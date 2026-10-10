// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	otherSession = model.SessionID(uuid.MustParse("7d1e3b2a-4c5f-4a6e-8b9d-0e1f2a3b4c5d"))
	thirdSession = model.SessionID(uuid.MustParse("019e1111-0000-7000-8000-000000000000"))
)

var sessionNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type mockSessionRepo struct{ mock.Mock }

func (m *mockSessionRepo) SaveSession(ctx context.Context, session model.Session) error {
	return m.Called(ctx, session).Error(0)
}

func (m *mockSessionRepo) LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(model.Session), args.Bool(1), args.Error(2)
}

func (m *mockSessionRepo) ListSessions(ctx context.Context) ([]model.Session, error) {
	args := m.Called(ctx)
	return args.Get(0).([]model.Session), args.Error(1)
}

func (m *mockSessionRepo) DeleteSession(ctx context.Context, id model.SessionID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *mockSessionRepo) ListSessionSavepoints(
	ctx context.Context, id model.SessionID,
) ([]model.ComposeProjectSavepoints, error) {
	args := m.Called(ctx, id)
	return args.Get(0).([]model.ComposeProjectSavepoints), args.Error(1)
}

func sessionRecord(id model.SessionID, name string, at time.Time) model.Session {
	return model.Session{ID: id, Name: name, CreatedAt: at}
}

func TestSessionService_ResolveSession(t *testing.T) {
	exp := sessionRecord(demoSession, "exp", sessionNow)
	other := sessionRecord(otherSession, "other", sessionNow)
	// A session named with other's id: names are tried first, so it wins.
	shadow := sessionRecord(thirdSession, otherSession.String(), sessionNow)
	unknownUUID := "019e0000-0000-7000-8000-000000000000"
	tests := []struct {
		name    string
		ref     string
		records []model.Session // what ListSessions returns; nil expects no listing
		want    model.Session
		wantErr error
		wantIDs []model.SessionID
	}{
		{name: "empty is the default", ref: "", want: model.DefaultSessionRecord},
		{name: "the default by name", ref: "baseline",
			records: []model.Session{model.DefaultSessionRecord, exp}, want: model.DefaultSessionRecord},
		{name: "the default by id", ref: model.DefaultSession.String(),
			records: []model.Session{model.DefaultSessionRecord, exp}, want: model.DefaultSessionRecord},
		{name: "a name", ref: "exp", records: []model.Session{model.DefaultSessionRecord, exp}, want: exp},
		{name: "an id", ref: demoSession.String(), records: []model.Session{model.DefaultSessionRecord, exp}, want: exp},
		{name: "a uuid-shaped name wins over the id it shadows", ref: otherSession.String(),
			records: []model.Session{model.DefaultSessionRecord, other, shadow}, want: shadow},
		{name: "an unknown uuid", ref: unknownUUID,
			records: []model.Session{model.DefaultSessionRecord, exp}, wantErr: model.ErrSessionNotFound},
		{name: "an unknown name", ref: "nope",
			records: []model.Session{model.DefaultSessionRecord, exp}, wantErr: model.ErrSessionNotFound},
		{name: "a name two sessions carry", ref: "exp",
			records: []model.Session{model.DefaultSessionRecord, exp, sessionRecord(otherSession, "exp", sessionNow)},
			wantIDs: []model.SessionID{demoSession, otherSession}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSessionRepo{}
			if tt.records != nil {
				repo.On("ListSessions", mock.Anything).Return(tt.records, nil).Once()
			}
			svc := NewSessionService(SessionDeps{Sessions: repo})

			// Act
			got, err := svc.ResolveSession(context.Background(), tt.ref)

			// Assert
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantIDs != nil:
				var ambiguous *model.AmbiguousSessionNameError
				require.ErrorAs(t, err, &ambiguous)
				assert.Equal(t, tt.wantIDs, ambiguous.IDs)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			repo.AssertExpectations(t)
		})
	}
}

func TestSessionService_ListSessions(t *testing.T) {
	// Arrange: records come back in any order and are listed oldest first; the default has
	// no creation time, so it leads.
	older := sessionRecord(otherSession, "old", sessionNow.Add(-time.Hour))
	newer := sessionRecord(demoSession, "new", sessionNow)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{newer, model.DefaultSessionRecord, older}, nil).Once()
	safety := model.Savepoint{Labels: map[string]string{model.SafetySnapshotLabel: model.SafetySnapshotLabelValue}}
	repo.On("ListSessionSavepoints", mock.Anything, model.DefaultSession).Return([]model.ComposeProjectSavepoints{
		{ComposeProject: "blog", Savepoints: []model.Savepoint{{}, safety}},
		{ComposeProject: "shop", Savepoints: []model.Savepoint{{}}},
	}, nil).Once()
	repo.On("ListSessionSavepoints", mock.Anything, otherSession).Return([]model.ComposeProjectSavepoints(nil), nil).Once()
	repo.On("ListSessionSavepoints", mock.Anything, demoSession).Return([]model.ComposeProjectSavepoints{
		{ComposeProject: "shop", Savepoints: []model.Savepoint{{}}},
	}, nil).Once()
	svc := NewSessionService(SessionDeps{Sessions: repo})

	// Act
	got, err := svc.ListSessions(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.SessionSummary{
		{Session: model.DefaultSessionRecord, SavepointCount: 2,
			ComposeProjects: []model.DockerComposeProjectName{"blog", "shop"}},
		{Session: older, ComposeProjects: []model.DockerComposeProjectName{}},
		{Session: newer, SavepointCount: 1, ComposeProjects: []model.DockerComposeProjectName{"shop"}},
	}, got)
	repo.AssertExpectations(t)
}

func TestSessionService_CreateSession(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	repo := &mockSessionRepo{}
	list := repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "other", sessionNow)}, nil).Once()
	save := repo.On("SaveSession", mock.Anything, mock.MatchedBy(func(s model.Session) bool {
		return s.ID.Validate() == nil && s.ID != model.DefaultSession &&
			s.Name == "feat/x" && s.CreatedAt.Equal(sessionNow) && s.From == nil
	})).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, list, save, release)
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker, Now: func() time.Time { return sessionNow }})

	// Act
	got, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: "feat/x"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "feat/x", got.Name)
	assert.Equal(t, sessionNow, got.CreatedAt)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_NameTaken(t *testing.T) {
	records := []model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "exp", sessionNow)}
	tests := []struct {
		name    string
		reqName string
	}{
		{name: "another session's name", reqName: "exp"},
		{name: "the default session's name", reqName: "baseline"},
		{name: "another session's id", reqName: otherSession.String()},
		{name: "the default session's id", reqName: model.DefaultSession.String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return(records, nil).Once()
			svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})

			// Act
			_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: tt.reqName})

			// Assert
			require.ErrorIs(t, err, model.ErrSessionNameTaken)
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_CreateSession_UUIDShapedName(t *testing.T) {
	// Arrange: a name shaped like a uuid that is no session's id is just a name.
	name := "019e0000-0000-7000-8000-000000000000"
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("SaveSession", mock.Anything, mock.MatchedBy(func(s model.Session) bool { return s.Name == name })).
		Return(nil).Once()
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker, Now: func() time.Time { return sessionNow }})

	// Act
	got, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: name})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_BlankNameTakesNoLock(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	repo := &mockSessionRepo{}
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})

	// Act
	_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: "  "})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSessionName)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_DeleteSession(t *testing.T) {
	// Arrange: a marker of another session does not stand in the way.
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	repo := &mockSessionRepo{}
	load := repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
	inflight := &fakeInflight{}
	marker := demoMarker(model.InflightRestoring)
	marker.Session = otherSession
	require.NoError(t, inflight.Put(context.Background(), marker))
	del := repo.On("DeleteSession", mock.Anything, demoSession).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, load, del, release)
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker, Inflight: inflight})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	got, err := svc.DeleteSession(ctx, demoSession.String())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, demoSession, got)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_DeleteSession_Refused(t *testing.T) {
	tests := []struct {
		name   string
		ref    string
		marker *model.InflightMarker
		setup  func(repo *mockSessionRepo)
		check  func(t *testing.T, err error)
	}{
		{
			name: "the default session",
			ref:  "baseline",
			setup: func(repo *mockSessionRepo) {
				repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, model.ErrDefaultSessionDelete) },
		},
		{
			name: "a session an interrupted rollback names",
			ref:  "exp",
			marker: func() *model.InflightMarker {
				m := demoMarker(model.InflightRestoring)
				m.Session = demoSession
				return &m
			}(),
			setup: func(repo *mockSessionRepo) {
				repo.On("ListSessions", mock.Anything).
					Return([]model.Session{sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
			},
			check: func(t *testing.T, err error) {
				var interrupted *model.InterruptedRollbackError
				require.ErrorAs(t, err, &interrupted)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			tt.setup(repo)
			inflight := &fakeInflight{}
			if tt.marker != nil {
				require.NoError(t, inflight.Put(context.Background(), *tt.marker))
			}
			svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker, Inflight: inflight})

			// Act
			_, err := svc.DeleteSession(context.Background(), tt.ref)

			// Assert
			tt.check(t, err)
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}
