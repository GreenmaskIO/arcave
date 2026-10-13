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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
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

func (m *mockSessionRepo) CopySession(
	ctx context.Context, from, to model.SessionID, except ...string,
) (int64, error) {
	args := m.Called(ctx, from, to, except)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockSessionRepo) IsSessionTreeEmpty(ctx context.Context, id model.SessionID) (bool, error) {
	args := m.Called(ctx, id)
	return args.Bool(0), args.Error(1)
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

func (m *mockSessionRepo) SessionSize(ctx context.Context, id model.SessionID) (int64, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(int64), args.Error(1)
}

// mockHeartbeats records the heartbeats written into each session's tree.
type mockHeartbeats struct{ mock.Mock }

func (m *mockHeartbeats) Write(ctx context.Context, id model.SessionID, h heartbeat.Heartbeat) error {
	return m.Called(ctx, id, h).Error(0)
}

// writerFor binds the heartbeat writes of session id.
func (m *mockHeartbeats) writerFor(id model.SessionID) (heartbeatWriter, error) {
	return sessionBeats{beats: m, id: id}, nil
}

type sessionBeats struct {
	beats *mockHeartbeats
	id    model.SessionID
}

func (w sessionBeats) Write(ctx context.Context, h heartbeat.Heartbeat) error {
	return w.beats.Write(ctx, w.id, h)
}

var testRunnerInfo = heartbeat.RunnerInfo{Host: "box", PID: 42, ArcaveVersion: "dev"}

// beatOf matches a heartbeat write of status.
func beatOf(status heartbeat.Status) any {
	return mock.MatchedBy(func(h heartbeat.Heartbeat) bool { return h.Status == status })
}

// forkBeatOf matches a fork heartbeat from otherSession, written by testRunnerInfo with status.
func forkBeatOf(status heartbeat.Status) any {
	data := `{"from":"` + otherSession.String() + `"}`
	return mock.MatchedBy(func(h heartbeat.Heartbeat) bool {
		return h.Operation == model.HeartbeatOperationFork && string(h.Data) == data &&
			h.RunnerInfo == testRunnerInfo && h.Status == status && !h.StartedAt.IsZero() &&
			(status == heartbeat.StatusInProgress) == h.FinishedAt.IsZero()
	})
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
	repo.On("SessionSize", mock.Anything, model.DefaultSession).Return(int64(2048), nil).Once()
	repo.On("SessionSize", mock.Anything, otherSession).Return(int64(0), nil).Once()
	repo.On("SessionSize", mock.Anything, demoSession).Return(int64(512), nil).Once()
	svc := NewSessionService(SessionDeps{Sessions: repo})

	// Act
	got, err := svc.ListSessions(context.Background())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.SessionSummary{
		{Session: model.DefaultSessionRecord, SavepointCount: 2,
			ComposeProjects: []model.DockerComposeProjectName{"blog", "shop"}, SizeBytes: 2048},
		{Session: older, ComposeProjects: []model.DockerComposeProjectName{}},
		{Session: newer, SavepointCount: 1, ComposeProjects: []model.DockerComposeProjectName{"shop"}, SizeBytes: 512},
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
			s.Name == "feat/x" && s.From == nil
	})).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, list, save, release)
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})
	before := time.Now()

	// Act
	got, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: "feat/x"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "feat/x", got.Name)
	assert.WithinRange(t, got.CreatedAt, before, time.Now())
	assert.Equal(t, time.UTC, got.CreatedAt.Location())
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_Clone(t *testing.T) {
	// Arrange: the source is named, the copy lands under the new id beside a heartbeat, the
	// record comes last and the heartbeat ends done.
	records := []model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "exp", sessionNow)}
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	repo := &mockSessionRepo{}
	beats := &mockHeartbeats{}
	list := repo.On("ListSessions", mock.Anything).Return(records, nil).Once()
	var clone model.SessionID
	empty := repo.On("IsSessionTreeEmpty", mock.Anything, mock.MatchedBy(func(id model.SessionID) bool {
		return id.Validate() == nil && id != otherSession && id != model.DefaultSession
	})).Run(func(args mock.Arguments) { clone = args.Get(1).(model.SessionID) }).Return(true, nil).Once()
	isClone := mock.MatchedBy(func(id model.SessionID) bool { return id == clone })
	beat := beats.On("Write", mock.Anything, isClone, forkBeatOf(heartbeat.StatusInProgress)).Return(nil).Once()
	cp := repo.On("CopySession", mock.Anything, otherSession, isClone, []string{heartbeat.FileName}).Return(int64(4096), nil).Once()
	save := repo.On("SaveSession", mock.Anything, mock.MatchedBy(func(s model.Session) bool {
		return s.ID == clone && s.Name == "feat/x" && s.From != nil && *s.From == model.SessionOrigin{Session: otherSession}
	})).Return(nil).Once()
	done := beats.On("Write", mock.Anything, isClone, forkBeatOf(heartbeat.StatusDone)).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, list, empty, beat, cp, save, done, release)
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker, RunnerInfo: testRunnerInfo})
	svc.heartbeatWriter = beats.writerFor

	// Act
	got, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: "feat/x", From: "exp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, clone, got.ID)
	assert.Equal(t, &model.SessionOrigin{Session: otherSession}, got.From)
	repo.AssertExpectations(t)
	beats.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_CloneOfBaselineWithoutName(t *testing.T) {
	// Arrange: no name means no name check; baseline has no record, but has a tree.
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	beats := &mockHeartbeats{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("IsSessionTreeEmpty", mock.Anything, mock.Anything).Return(true, nil).Once()
	beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusInProgress)).Return(nil).Once()
	repo.On("CopySession", mock.Anything, model.DefaultSession, mock.Anything, []string{heartbeat.FileName}).Return(int64(0), nil).Once()
	repo.On("SaveSession", mock.Anything, mock.MatchedBy(func(s model.Session) bool {
		return s.From != nil && s.From.Session == model.DefaultSession
	})).Return(nil).Once()
	beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusDone)).Return(nil).Once()
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})
	svc.heartbeatWriter = beats.writerFor

	// Act
	_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{From: "baseline"})

	// Assert
	require.NoError(t, err)
	repo.AssertExpectations(t)
	beats.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_CloneRefusedBeforeCopy(t *testing.T) {
	records := []model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "exp", sessionNow)}
	tests := []struct {
		name    string
		req     model.CreateSessionRequest
		wantErr error
	}{
		{name: "a taken name", req: model.CreateSessionRequest{Name: "exp", From: "baseline"},
			wantErr: model.ErrSessionNameTaken},
		{name: "an unknown source", req: model.CreateSessionRequest{Name: "feat/x", From: "nope"},
			wantErr: model.ErrSessionNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: strict mocks, so a CopySession call fails the test.
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return(records, nil).Once()
			svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})

			// Act
			_, err := svc.CreateSession(context.Background(), tt.req)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_CreateSession_CloneFailureLeavesTheTreeMarkedFailed(t *testing.T) {
	errCopy := errors.New("disk full")
	errSave := errors.New("record write failed")
	errMark := errors.New("mark failed")
	tests := []struct {
		name    string
		copyErr error
		saveErr error
		markErr error
		wantErr error
	}{
		{name: "the copy fails", copyErr: errCopy, wantErr: errCopy},
		{name: "the record fails", saveErr: errSave, wantErr: errSave},
		{name: "the mark fails too: the copy's error stands", copyErr: errCopy, markErr: errMark, wantErr: errCopy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: the command's context is cancelled, as by Ctrl-C; the mark must not be.
			// Strict mocks: a DeleteSession call fails the test, the tree is a gc's.
			ctx, cancel := context.WithCancel(context.Background())
			locker := &mockLocker{}
			acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
			repo := &mockSessionRepo{}
			beats := &mockHeartbeats{}
			resolve := repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
			var clone model.SessionID
			empty := repo.On("IsSessionTreeEmpty", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) { clone = args.Get(1).(model.SessionID) }).Return(true, nil).Once()
			beat := beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusInProgress)).Return(nil).Once()
			cp := repo.On("CopySession", mock.Anything, model.DefaultSession, mock.Anything, []string{heartbeat.FileName}).
				Run(func(mock.Arguments) { cancel() }).Return(int64(1), tt.copyErr).Once()
			calls := []*mock.Call{acquire, resolve, empty, beat, cp}
			if tt.copyErr == nil {
				calls = append(calls, repo.On("SaveSession", mock.Anything, mock.Anything).Return(tt.saveErr).Once())
			}
			mark := beats.On("Write", mock.MatchedBy(func(c context.Context) bool { return c.Err() == nil }),
				mock.MatchedBy(func(id model.SessionID) bool { return id == clone }),
				mock.MatchedBy(func(h heartbeat.Heartbeat) bool {
					return h.Status == heartbeat.StatusFailed && strings.Contains(h.Error, tt.wantErr.Error()) &&
						!h.FinishedAt.IsZero()
				})).Return(tt.markErr).Once()
			release := locker.On("Release").Return(nil).Once()
			mock.InOrder(append(calls, mark, release)...)
			svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})
			svc.heartbeatWriter = beats.writerFor

			// Act
			_, err := svc.CreateSession(ctx, model.CreateSessionRequest{From: "baseline"})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			if tt.markErr != nil {
				assert.NotErrorIs(t, err, tt.markErr)
			}
			repo.AssertExpectations(t)
			beats.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_CreateSession_OccupiedTreeWritesNothing(t *testing.T) {
	// Arrange: strict mocks, so a heartbeat or a copy fails the test; whatever the tree holds,
	// a heartbeat included, is another operation's.
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("IsSessionTreeEmpty", mock.Anything, mock.Anything).Return(false, nil).Once()
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})

	// Act
	_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{From: "baseline"})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionTreeOccupied)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_FirstHeartbeatFailsBeforeCopy(t *testing.T) {
	// Arrange: strict mocks, so a CopySession call fails the test. The first beat may have
	// landed despite its error, so it is marked failed.
	errPut := errors.New("store unreachable")
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	beats := &mockHeartbeats{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("IsSessionTreeEmpty", mock.Anything, mock.Anything).Return(true, nil).Once()
	first := beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusInProgress)).Return(errPut).Once()
	mark := beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusFailed)).Return(nil).Once()
	mock.InOrder(first, mark)
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})
	svc.heartbeatWriter = beats.writerFor

	// Act
	_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{From: "baseline"})

	// Assert
	require.ErrorIs(t, err, model.ErrSessionHeartbeatLost)
	require.ErrorIs(t, err, errPut)
	repo.AssertExpectations(t)
	beats.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_CreateSession_HeartbeatLostCancelsTheFork(t *testing.T) {
	tests := []struct {
		name    string
		copyErr error
	}{
		// The copy stops on the cancel; its own error gives way to the cause.
		{name: "the copy is cancelled", copyErr: errors.New("copy compose: context canceled")},
		// The copy finishes as the cancel lands: Alive refuses the commit.
		{name: "the copy finishes anyway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: only the first beat lands, so the deadline (stale age less one interval)
			// passes and a tick cancels the copy. Strict mocks: a SaveSession call fails the test.
			errPut := errors.New("store unreachable")
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			beats := &mockHeartbeats{}
			repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
			repo.On("IsSessionTreeEmpty", mock.Anything, mock.Anything).Return(true, nil).Once()
			beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusInProgress)).Return(nil).Once()
			beats.On("Write", mock.Anything, mock.Anything, beatOf(heartbeat.StatusInProgress)).Return(errPut).Maybe()
			repo.On("CopySession", mock.Anything, model.DefaultSession, mock.Anything, []string{heartbeat.FileName}).
				Run(func(args mock.Arguments) { <-args.Get(0).(context.Context).Done() }).
				Return(int64(1), tt.copyErr).Once()
			beats.On("Write", mock.Anything, mock.Anything, mock.MatchedBy(func(h heartbeat.Heartbeat) bool {
				return h.Status == heartbeat.StatusFailed && strings.Contains(h.Error, model.ErrSessionHeartbeatLost.Error())
			})).Return(nil).Once()
			svc := NewSessionService(SessionDeps{
				Sessions: repo, Locker: locker,
				HeartbeatInterval: 10 * time.Millisecond, HeartbeatStaleAfter: 30 * time.Millisecond,
			})
			svc.heartbeatWriter = beats.writerFor

			// Act
			_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{From: "baseline"})

			// Assert
			require.ErrorIs(t, err, model.ErrSessionHeartbeatLost)
			repo.AssertExpectations(t)
			beats.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
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
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker})

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
	size := repo.On("SessionSize", mock.Anything, demoSession).Return(int64(4096), nil).Once()
	del := repo.On("DeleteSession", mock.Anything, demoSession).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, load, size, del, release)
	svc := NewSessionService(SessionDeps{Sessions: repo, Locker: locker, Inflight: inflight})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	got, err := svc.DeleteSession(ctx, demoSession.String())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.DeleteSessionResponse{Session: demoSession, Deleted: true, FreedBytes: 4096}, got)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

var errSessionSize = errors.New("list failed")

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
		{
			name: "a session that cannot be measured",
			ref:  "exp",
			setup: func(repo *mockSessionRepo) {
				repo.On("ListSessions", mock.Anything).
					Return([]model.Session{sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
				repo.On("SessionSize", mock.Anything, demoSession).Return(int64(0), errSessionSize).Once()
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, errSessionSize) },
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
