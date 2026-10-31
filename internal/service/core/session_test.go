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
	"cmp"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
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
	// forkedSession is a fourth id, for listings that need one more tree.
	forkedSession = model.SessionID(uuid.MustParse("019e2222-0000-7000-8000-000000000000"))
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

func (m *mockSessionRepo) ListUncommittedSessions(ctx context.Context) ([]model.SessionID, error) {
	args := m.Called(ctx)
	return args.Get(0).([]model.SessionID), args.Error(1)
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

// fakeBeat is a tree's heartbeat as the reader returns it; found false is none.
type fakeBeat struct {
	h     heartbeat.Heartbeat
	found bool
	err   error
}

func (b fakeBeat) Read(context.Context) (heartbeat.Heartbeat, error) {
	if b.err != nil {
		return heartbeat.Heartbeat{}, b.err
	}
	if !b.found {
		return heartbeat.Heartbeat{}, model.ErrHeartbeatNotFound
	}
	return b.h, nil
}

// readBeats opens each session's heartbeat from beats, failing to read those in errs; a
// session in neither has none.
func readBeats(
	beats map[model.SessionID]heartbeat.Heartbeat, errs map[model.SessionID]error,
) func(model.SessionID) (heartbeatReader, error) {
	return func(id model.SessionID) (heartbeatReader, error) {
		h, found := beats[id]
		return fakeBeat{h: h, found: found, err: errs[id]}, nil
	}
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
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo})

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
	// Arrange: records come back in any order and asc lists them oldest first; the default has
	// no creation time, so it leads.
	older := sessionRecord(otherSession, "old", sessionNow.Add(-time.Hour))
	newer := sessionRecord(demoSession, "new", sessionNow)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{newer, model.DefaultSessionRecord, older}, nil).Once()
	safety := model.Savepoint{Labels: map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}}
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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo})
	svc.sessionSavepoints = repo.ListSessionSavepoints

	// Act
	got, err := svc.ListSessions(context.Background(), model.ListSessionsRequest{Sort: model.SortAsc})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.SessionSummary{
		{Session: model.DefaultSessionRecord, Status: model.SessionStatusReady, Current: true, SavepointCount: 2,
			ComposeProjects: []model.DockerComposeProjectName{"blog", "shop"}, SizeBytes: 2048},
		{Session: older, Status: model.SessionStatusReady, ComposeProjects: []model.DockerComposeProjectName{}},
		{Session: newer, Status: model.SessionStatusReady, SavepointCount: 1,
			ComposeProjects: []model.DockerComposeProjectName{"shop"}, SizeBytes: 512},
	}, got)
	repo.AssertExpectations(t)
}

func TestSessionService_ListSessions_Sort(t *testing.T) {
	older := sessionRecord(otherSession, "old", sessionNow.Add(-time.Hour))
	newer := sessionRecord(demoSession, "new", sessionNow)
	tests := []struct {
		name string
		sort model.SortOrder
		want []model.SessionID
	}{
		{name: "zero is newest first", want: []model.SessionID{demoSession, otherSession, model.DefaultSession}},
		{name: "desc", sort: model.SortDesc, want: []model.SessionID{demoSession, otherSession, model.DefaultSession}},
		{name: "asc", sort: model.SortAsc, want: []model.SessionID{model.DefaultSession, otherSession, demoSession}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return([]model.Session{newer, model.DefaultSessionRecord, older}, nil).Once()
			repo.On("ListSessionSavepoints", mock.Anything, mock.Anything).Return([]model.ComposeProjectSavepoints(nil), nil)
			repo.On("SessionSize", mock.Anything, mock.Anything).Return(int64(0), nil)
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo})
			svc.sessionSavepoints = repo.ListSessionSavepoints

			// Act
			got, err := svc.ListSessions(context.Background(), model.ListSessionsRequest{Sort: tt.sort})

			// Assert
			require.NoError(t, err)
			ids := make([]model.SessionID, len(got))
			for i, row := range got {
				ids[i] = row.ID
			}
			assert.Equal(t, tt.want, ids)
			repo.AssertExpectations(t)
		})
	}
}

func TestSessionService_ListSessions_All(t *testing.T) {
	// Arrange: one tree per heartbeat status, plus one that cannot be summarized and keeps its row.
	cloning, failed, done, none := thirdSession, demoSession, forkedSession, otherSession
	unreadable := model.SessionID(uuid.MustParse("019e4444-0000-7000-8000-000000000000"))
	otherOp := model.SessionID(uuid.MustParse("019e5555-0000-7000-8000-000000000000"))
	errRead := errors.New("store timeout")
	errDecode := errors.New("decode savepoint.json: unexpected EOF")
	data, err := heartbeat.EncodeData(model.ForkHeartbeatData{From: model.DefaultSession})
	require.NoError(t, err)
	fork := func(status heartbeat.Status, at time.Time, cause string) heartbeat.Heartbeat {
		return heartbeat.Heartbeat{Operation: model.HeartbeatOperationFork, Status: status, StartedAt: at, Error: cause,
			Data: data}
	}
	beats := map[model.SessionID]heartbeat.Heartbeat{
		cloning: fork(heartbeat.StatusInProgress, sessionNow.Add(3*time.Minute), ""),
		failed:  fork(heartbeat.StatusFailed, sessionNow.Add(2*time.Minute), "disk full"),
		done:    fork(heartbeat.StatusDone, sessionNow.Add(time.Minute), ""),
		otherOp: {Operation: "gc", Status: heartbeat.StatusInProgress, StartedAt: sessionNow.Add(4 * time.Minute)},
	}
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("ListUncommittedSessions", mock.Anything).
		Return([]model.SessionID{cloning, failed, done, none, unreadable, otherOp}, nil).Once()
	repo.On("ListSessionSavepoints", mock.Anything, cloning).Return([]model.ComposeProjectSavepoints(nil), errDecode).Once()
	repo.On("ListSessionSavepoints", mock.Anything, mock.Anything).Return([]model.ComposeProjectSavepoints(nil), nil)
	repo.On("SessionSize", mock.Anything, mock.Anything).Return(int64(0), nil)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo})
	svc.sessionSavepoints = repo.ListSessionSavepoints
	svc.heartbeatReader = readBeats(beats, map[model.SessionID]error{unreadable: errRead})
	origin := &model.SessionOrigin{Session: model.DefaultSession}
	row := func(id model.SessionID, at time.Time, from *model.SessionOrigin, status model.SessionStatus,
		cause string) model.SessionSummary {
		return model.SessionSummary{Session: model.Session{ID: id, CreatedAt: at, From: from}, Status: status,
			Error: cause, ComposeProjects: []model.DockerComposeProjectName{}}
	}

	// Act
	got, err := svc.ListSessions(context.Background(), model.ListSessionsRequest{All: true, Sort: model.SortAsc})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.SessionSummary{
		{Session: model.DefaultSessionRecord, Status: model.SessionStatusReady, Current: true,
			ComposeProjects: []model.DockerComposeProjectName{}},
		row(none, time.Time{}, nil, model.SessionStatusIncomplete, ""),
		row(unreadable, time.Time{}, nil, model.SessionStatusUnknown, errRead.Error()),
		row(done, sessionNow.Add(time.Minute), origin, model.SessionStatusIncomplete, ""),
		row(failed, sessionNow.Add(2*time.Minute), origin, model.SessionStatusFailed, "disk full"),
		row(cloning, sessionNow.Add(3*time.Minute), origin, model.SessionStatusCloning, ""),
		row(otherOp, sessionNow.Add(4*time.Minute), nil, model.SessionStatusUnknown, `a "gc" operation is in progress`),
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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})
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
	// Arrange: the source is named and locked, the copy lands under the new id beside a heartbeat,
	// the record comes last once the source's lock still holds, and the heartbeat ends done.
	records := []model.Session{model.DefaultSessionRecord, sessionRecord(otherSession, "exp", sessionNow)}
	locker := &mockLocker{}
	acquire := locker.On("Acquire", mock.Anything).Return(nil).Once()
	repo := &mockSessionRepo{}
	beats := &mockHeartbeats{}
	list := repo.On("ListSessions", mock.Anything).Return(records, nil).Once()
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSessionFork).Return(grantedLock, nil).Once()
	// The source is checked again under its lock: it may have gone while the fork waited.
	check := repo.On("LoadSession", mock.Anything, otherSession).Return(records[1], true, nil).Once()
	var clone model.SessionID
	empty := repo.On("IsSessionTreeEmpty", mock.Anything, mock.MatchedBy(func(id model.SessionID) bool {
		return id.Validate() == nil && id != otherSession && id != model.DefaultSession
	})).Run(func(args mock.Arguments) { clone = args.Get(1).(model.SessionID) }).Return(true, nil).Once()
	isClone := mock.MatchedBy(func(id model.SessionID) bool { return id == clone })
	beat := beats.On("Write", mock.Anything, isClone, forkBeatOf(heartbeat.StatusInProgress)).Return(nil).Once()
	cp := repo.On("CopySession", mock.Anything, otherSession, isClone, []string{heartbeat.FileName}).Return(int64(4096), nil).Once()
	verify := grantedLock.On("Verify", mock.Anything).Return(nil).Once()
	save := repo.On("SaveSession", mock.Anything, mock.MatchedBy(func(s model.Session) bool {
		return s.ID == clone && s.Name == "feat/x" && s.From != nil && *s.From == model.SessionOrigin{Session: otherSession}
	})).Return(nil).Once()
	done := beats.On("Write", mock.Anything, isClone, forkBeatOf(heartbeat.StatusDone)).Return(nil).Once()
	free := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, list, lock, check, empty, beat, cp, verify, save, done, free, release)
	var lockedSession model.SessionID
	newLocker := func(id model.SessionID) (model.SessionLocker, error) {
		lockedSession = id
		return sessionLocker, nil
	}
	svc := NewSessionService(SessionDeps{NewSessionLocker: newLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker, RunnerInfo: testRunnerInfo})
	svc.heartbeatWriter = beats.writerFor

	// Act
	got, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: "feat/x", From: "exp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, clone, got.ID)
	assert.Equal(t, &model.SessionOrigin{Session: otherSession}, got.From)
	assert.Equal(t, otherSession, lockedSession, "the fork locks its source, not the new session")
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})
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
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})

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
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})
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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})

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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})
	svc.heartbeatWriter = beats.writerFor

	// Act
	_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{From: "baseline"})

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatLost)
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
				return h.Status == heartbeat.StatusFailed && strings.Contains(h.Error, model.ErrHeartbeatLost.Error())
			})).Return(nil).Once()
			svc := NewSessionService(SessionDeps{
				NewSessionLocker: newFakeSessionLocker,
				Sessions:         repo, Locker: locker,
				HeartbeatInterval: 10 * time.Millisecond, HeartbeatStaleAfter: 30 * time.Millisecond,
			})
			svc.heartbeatWriter = beats.writerFor

			// Act
			_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{From: "baseline"})

			// Assert
			require.ErrorIs(t, err, model.ErrHeartbeatLost)
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
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})

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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})

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
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker})

	// Act
	_, err := svc.CreateSession(context.Background(), model.CreateSessionRequest{Name: "  "})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSessionName)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_RemoveSession(t *testing.T) {
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
	sessionLocker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{}
	lock := sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSessionRemove).Return(grantedLock, nil).Once()
	size := repo.On("SessionSize", mock.Anything, demoSession).Return(int64(4096), nil).Once()
	verify := grantedLock.On("Verify", mock.Anything).Return(nil).Once()
	del := repo.On("DeleteSession", mock.Anything, demoSession).Return(nil).Once()
	free := grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	release := locker.On("Release").Return(nil).Once()
	mock.InOrder(acquire, load, lock, size, verify, del, free, release)
	var lockedSession model.SessionID
	newLocker := func(id model.SessionID) (model.SessionLocker, error) {
		lockedSession = id
		return sessionLocker, nil
	}
	svc := NewSessionService(SessionDeps{NewSessionLocker: newLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker, Inflight: inflight})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	got, err := svc.RemoveSession(ctx, model.RemoveSessionRequest{Session: demoSession.String()})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.RemoveSessionResponse{Session: demoSession, Removed: true, FreedBytes: 4096}, got)
	assert.Equal(t, demoSession, lockedSession)
	sessionLocker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// expectSessionDeleted expects id's tree deleted.
func expectSessionDeleted(repo *mockSessionRepo, id model.SessionID) {
	repo.On("DeleteSession", mock.Anything, id).Return(nil).Once()
}

var errSessionSize = errors.New("list failed")

func TestSessionService_RemoveSession_Refused(t *testing.T) {
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
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker, Inflight: inflight})

			// Act
			_, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: tt.ref})

			// Assert
			tt.check(t, err)
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_RemoveSession_Uncommitted(t *testing.T) {
	tests := []struct {
		name    string
		status  heartbeat.Status // the heartbeat's; empty is none
		op      model.HeartbeatOperation
		readErr error
		force   bool
		wantErr error
	}{
		{name: "a failed fork", status: heartbeat.StatusFailed},
		{name: "a tree without a heartbeat"},
		{name: "a fork done but never committed", status: heartbeat.StatusDone},
		{name: "a fork still cloning is refused", status: heartbeat.StatusInProgress, wantErr: model.ErrSessionCloning},
		{name: "a fork still cloning, forced", status: heartbeat.StatusInProgress, force: true},
		{name: "an unreadable heartbeat is refused", readErr: errors.New("store timeout"),
			wantErr: model.ErrSessionStatusUnknown},
		{name: "an unreadable heartbeat, forced", readErr: errors.New("store timeout"), force: true},
		{name: "another operation in progress is refused", status: heartbeat.StatusInProgress, op: "gc",
			wantErr: model.ErrSessionStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: strict mocks, so a refused delete that reaches the store fails the test.
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
			repo.On("ListUncommittedSessions", mock.Anything).Return([]model.SessionID{demoSession}, nil).Once()
			if tt.wantErr == nil {
				repo.On("SessionSize", mock.Anything, demoSession).Return(int64(10), nil).Once()
				expectSessionDeleted(repo, demoSession)
			}
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})
			beats := map[model.SessionID]heartbeat.Heartbeat{}
			if tt.status != "" {
				beats[demoSession] = heartbeat.Heartbeat{Operation: cmp.Or(tt.op, model.HeartbeatOperationFork), Status: tt.status}
			}
			svc.heartbeatReader = readBeats(beats, map[model.SessionID]error{demoSession: tt.readErr})

			// Act
			got, err := svc.RemoveSession(context.Background(),
				model.RemoveSessionRequest{Session: demoSession.String(), Force: tt.force})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, model.RemoveSessionResponse{Session: demoSession, Removed: true, FreedBytes: 10}, got)
			}
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_RemoveSession_NotFound(t *testing.T) {
	tests := []struct {
		name  string
		ref   string
		setup func(repo *mockSessionRepo)
	}{
		// A name is never a tree's: only an id is looked for among them.
		{name: "a name no session carries", ref: "ghost"},
		{name: "an id no tree carries", ref: demoSession.String(), setup: func(repo *mockSessionRepo) {
			repo.On("ListUncommittedSessions", mock.Anything).Return([]model.SessionID{otherSession}, nil).Once()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
			if tt.setup != nil {
				tt.setup(repo)
			}
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{}, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})

			// Act
			_, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: tt.ref})

			// Assert
			require.ErrorIs(t, err, model.ErrSessionNotFound)
			repo.AssertExpectations(t)
		})
	}
}

func TestSessionService_ResolveSession_Current(t *testing.T) {
	exp := sessionRecord(demoSession, "exp", sessionNow)
	errPointer := errors.New("decoding the current session")
	tests := []struct {
		name    string
		pointer *fakePointer
		list    bool // whether ListSessions is expected
		want    model.Session
		wantErr error
	}{
		{name: "no pointer is the default", pointer: &fakePointer{}, want: model.DefaultSessionRecord},
		{name: "a pointer to a session", pointer: pointerAt(demoSession), list: true, want: exp},
		{name: "a pointer to the default", pointer: pointerAt(model.DefaultSession), list: true,
			want: model.DefaultSessionRecord},
		{name: "a pointer to a deleted session is refused", pointer: pointerAt(otherSession), list: true,
			wantErr: model.ErrCurrentSessionNotFound},
		{name: "an unreadable pointer is refused", pointer: &fakePointer{err: errPointer}, wantErr: errPointer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSessionRepo{}
			if tt.list {
				repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord, exp}, nil).Once()
			}
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: tt.pointer, Sessions: repo})

			// Act
			got, err := svc.ResolveSession(context.Background(), "")

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			repo.AssertExpectations(t)
		})
	}
}

func TestSessionService_ResolveSession_RefOutranksThePointer(t *testing.T) {
	// Arrange
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointerAt(otherSession), Sessions: repo})

	// Act
	got, err := svc.ResolveSession(context.Background(), "baseline")

	// Assert: the dangling pointer is never read.
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSessionRecord, got)
	repo.AssertExpectations(t)
}

func TestSessionService_SetSession(t *testing.T) {
	exp := sessionRecord(demoSession, "exp", sessionNow)
	tests := []struct {
		name    string
		ref     string
		want    *model.SessionID // the pointer afterwards; nil is none
		wantErr error
	}{
		{name: "by name", ref: "exp", want: &demoSession},
		{name: "by id", ref: demoSession.String(), want: &demoSession},
		{name: "the default", ref: "baseline", want: &model.DefaultSession},
		{name: "an unknown session leaves the pointer", ref: "ghost", wantErr: model.ErrSessionNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord, exp}, nil).Once()
			pointer := &fakePointer{}
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: repo, Locker: locker})

			// Act
			got, err := svc.SetSession(context.Background(), tt.ref)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, pointer.id)
			} else {
				require.NoError(t, err)
				assert.Equal(t, *tt.want, got.ID)
				require.NotNil(t, pointer.id)
				assert.Equal(t, *tt.want, *pointer.id)
			}
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_SetSession_Locked(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	locker.On("Acquire", mock.Anything).Return(model.ErrProjectLocked).Once()
	pointer := &fakePointer{}
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: &mockSessionRepo{}, Locker: locker})

	// Act
	_, err := svc.SetSession(context.Background(), "exp")

	// Assert
	require.ErrorIs(t, err, model.ErrProjectLocked)
	assert.Nil(t, pointer.id)
	locker.AssertExpectations(t)
}

func TestSessionService_UnsetSession(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	pointer := pointerAt(otherSession)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: &mockSessionRepo{}, Locker: locker})

	// Act
	got, err := svc.UnsetSession(context.Background())

	// Assert: a dangling pointer is cleared without being resolved.
	require.NoError(t, err)
	assert.Equal(t, model.DefaultSessionRecord, got)
	assert.Nil(t, pointer.id)
	locker.AssertExpectations(t)
}

func TestSessionService_RemoveSession_Current(t *testing.T) {
	tests := []struct {
		name        string
		force       bool
		wantErr     error
		wantPointer *model.SessionID
	}{
		{name: "refused without force", wantErr: model.ErrCurrentSessionDelete, wantPointer: &demoSession},
		{name: "forced, the pointer is cleared", force: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: strict mocks, so a refused delete that reaches the store fails the test.
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).
				Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
			if tt.wantErr == nil {
				repo.On("SessionSize", mock.Anything, demoSession).Return(int64(10), nil).Once()
				expectSessionDeleted(repo, demoSession)
			}
			pointer := pointerAt(demoSession)
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})

			// Act
			got, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: "exp", Force: tt.force})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, model.RemoveSessionResponse{Session: demoSession, Removed: true, FreedBytes: 10}, got)
			}
			assert.Equal(t, tt.wantPointer, pointer.id)
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_RemoveSession_NotCurrentKeepsThePointer(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
	repo.On("SessionSize", mock.Anything, demoSession).Return(int64(10), nil).Once()
	expectSessionDeleted(repo, demoSession)
	pointer := pointerAt(otherSession)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})

	// Act
	_, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: "exp"})

	// Assert
	require.NoError(t, err)
	require.NotNil(t, pointer.id)
	assert.Equal(t, otherSession, *pointer.id)
	repo.AssertExpectations(t)
}

func TestSessionService_RemoveSession_ForcedDefaultKeepsThePointer(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("SessionSize", mock.Anything, model.DefaultSession).Return(int64(10), nil).Once()
	expectSessionDeleted(repo, model.DefaultSession)
	pointer := pointerAt(model.DefaultSession)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})

	// Act
	got, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: model.DefaultSessionName, Force: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.RemoveSessionResponse{Session: model.DefaultSession, Removed: true, FreedBytes: 10}, got)
	require.NotNil(t, pointer.id)
	assert.Equal(t, model.DefaultSession, *pointer.id)
	repo.AssertExpectations(t)
}

func TestSessionService_ListSessions_MarksTheCurrentSession(t *testing.T) {
	// Arrange
	exp := sessionRecord(demoSession, "exp", sessionNow)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord, exp}, nil).Once()
	repo.On("ListSessionSavepoints", mock.Anything, mock.Anything).Return([]model.ComposeProjectSavepoints(nil), nil)
	repo.On("SessionSize", mock.Anything, mock.Anything).Return(int64(0), nil)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointerAt(demoSession), Sessions: repo})
	svc.sessionSavepoints = repo.ListSessionSavepoints

	// Act
	got, err := svc.ListSessions(context.Background(), model.ListSessionsRequest{Sort: model.SortAsc})

	// Assert
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.False(t, got[0].Current, "the default is not current while the pointer names another")
	assert.True(t, got[1].Current)
	repo.AssertExpectations(t)
}

func TestSessionService_EmptyRefIsRefused(t *testing.T) {
	// Arrange: strict mocks, so a refusal that takes the lock or lists sessions fails the test.
	ctx := context.Background()
	pointer := pointerAt(demoSession)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: &mockSessionRepo{}, Locker: &mockLocker{}})

	// Act
	_, setErr := svc.SetSession(ctx, "")
	_, deleteErr := svc.RemoveSession(ctx, model.RemoveSessionRequest{Session: "", Force: true})

	// Assert: an empty ref never stands for the current session where it would change something.
	require.ErrorIs(t, setErr, model.ErrNoSessionRef)
	require.ErrorIs(t, deleteErr, model.ErrNoSessionRef)
	require.NotNil(t, pointer.id)
	assert.Equal(t, demoSession, *pointer.id)
}

var errPointerUnreadable = errors.New(`decoding the current session: invalid session id "feat"`)

func TestSessionService_ResolveSession_UnreadablePointerNamesTheWayOut(t *testing.T) {
	// Arrange
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{err: errPointerUnreadable}, Sessions: &mockSessionRepo{}})

	// Act
	_, err := svc.ResolveSession(context.Background(), "")

	// Assert
	require.ErrorIs(t, err, errPointerUnreadable)
	assert.Contains(t, err.Error(), "arcave session unset")
}

func TestSessionService_ListSessions_UnreadablePointerMarksNone(t *testing.T) {
	// Arrange
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord}, nil).Once()
	repo.On("ListSessionSavepoints", mock.Anything, model.DefaultSession).Return([]model.ComposeProjectSavepoints(nil), nil).Once()
	repo.On("SessionSize", mock.Anything, model.DefaultSession).Return(int64(0), nil).Once()
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{err: errPointerUnreadable}, Sessions: repo})
	svc.sessionSavepoints = repo.ListSessionSavepoints

	// Act
	got, err := svc.ListSessions(context.Background(), model.ListSessionsRequest{})

	// Assert: the listing is how a broken pointer is diagnosed, so it still answers.
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].Current)
	repo.AssertExpectations(t)
}

func TestSessionService_RemoveSession_UnreadablePointerNamesNoSession(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
	repo.On("SessionSize", mock.Anything, demoSession).Return(int64(10), nil).Once()
	expectSessionDeleted(repo, demoSession)
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: &fakePointer{err: errPointerUnreadable}, Sessions: repo,
		Locker: locker, Inflight: &fakeInflight{}})

	// Act
	got, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: "exp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, demoSession, got.Session)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

func TestSessionService_ListSessions_Ref(t *testing.T) {
	exp := sessionRecord(demoSession, "exp", sessionNow)
	tests := []struct {
		name    string
		ref     string
		pointer *fakePointer
		want    []bool // Current per row: baseline, exp
	}{
		{name: "no ref, no pointer: the default", pointer: &fakePointer{}, want: []bool{true, false}},
		{name: "no ref: the pointer", pointer: pointerAt(demoSession), want: []bool{false, true}},
		{name: "a ref by name outranks the pointer", ref: "baseline", pointer: pointerAt(demoSession),
			want: []bool{true, false}},
		{name: "a ref by id", ref: demoSession.String(), pointer: &fakePointer{}, want: []bool{false, true}},
		{name: "a ref naming nothing marks none", ref: "ghost", pointer: pointerAt(demoSession),
			want: []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).Return([]model.Session{model.DefaultSessionRecord, exp}, nil).Once()
			repo.On("ListSessionSavepoints", mock.Anything, mock.Anything).Return([]model.ComposeProjectSavepoints(nil), nil)
			repo.On("SessionSize", mock.Anything, mock.Anything).Return(int64(0), nil)
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: tt.pointer, Sessions: repo})
			svc.sessionSavepoints = repo.ListSessionSavepoints

			// Act
			got, err := svc.ListSessions(context.Background(), model.ListSessionsRequest{Ref: tt.ref, Sort: model.SortAsc})

			// Assert
			require.NoError(t, err)
			current := make([]bool, len(got))
			for i, row := range got {
				current[i] = row.Current
			}
			assert.Equal(t, tt.want, current)
			repo.AssertExpectations(t)
		})
	}
}

func TestSessionService_RemoveSession_Pinned(t *testing.T) {
	tests := []struct {
		name    string
		pinned  model.SessionID
		force   bool
		wantErr error
	}{
		{name: "the pinned session is refused", pinned: demoSession, wantErr: model.ErrCurrentSessionDelete},
		{name: "the pinned session, forced", pinned: demoSession, force: true},
		{name: "a pin elsewhere does not block it", pinned: otherSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: the pointer names a third session and must survive every case.
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSessionRepo{}
			repo.On("ListSessions", mock.Anything).
				Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
			if tt.wantErr == nil {
				repo.On("SessionSize", mock.Anything, demoSession).Return(int64(10), nil).Once()
				expectSessionDeleted(repo, demoSession)
			}
			pointer := pointerAt(thirdSession)
			svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})

			// Act
			_, err := svc.RemoveSession(context.Background(),
				model.RemoveSessionRequest{Session: "exp", Force: tt.force, Pinned: tt.pinned})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Contains(t, err.Error(), "pinned")
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, pointer.id)
			assert.Equal(t, thirdSession, *pointer.id)
			repo.AssertExpectations(t)
			locker.AssertExpectations(t)
		})
	}
}

func TestSessionService_RemoveSession_PointerClearFailureKeepsTheResult(t *testing.T) {
	// Arrange
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSessionRepo{}
	repo.On("ListSessions", mock.Anything).
		Return([]model.Session{model.DefaultSessionRecord, sessionRecord(demoSession, "exp", sessionNow)}, nil).Once()
	repo.On("SessionSize", mock.Anything, demoSession).Return(int64(10), nil).Once()
	expectSessionDeleted(repo, demoSession)
	pointer := pointerAt(demoSession)
	pointer.clearErr = errors.New("permission denied")
	svc := NewSessionService(SessionDeps{NewSessionLocker: newFakeSessionLocker, Current: pointer, Sessions: repo, Locker: locker, Inflight: &fakeInflight{}})

	// Act
	got, err := svc.RemoveSession(context.Background(), model.RemoveSessionRequest{Session: "exp", Force: true})

	// Assert: the delete happened, so it is reported; the dangling pointer is the next command's to name.
	require.NoError(t, err)
	assert.Equal(t, model.RemoveSessionResponse{Session: demoSession, Removed: true, FreedBytes: 10}, got)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// A session's savepoints are read compose project by compose project, in name order: one name in
// two compose projects is two savepoints, and one holding none is left out.
func TestReadSessionSavepoints(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store := inmemory.New("")
	for _, composeProject := range []string{"shop", "blog", "cache"} {
		key := "sessions/" + demoSession.String() + "/compose/" + composeProject + "/019e843e-1650-7000-8000-000000000300/project.yaml"
		require.NoError(t, store.PutObject(ctx, key, strings.NewReader("services: {}")))
	}
	shop := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000100", Name: "v1"}
	blog := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000200", Name: "v1"}
	blogSavepoints, cacheSavepoints, shopSavepoints := &mockSavepointReader{}, &mockSavepointReader{}, &mockSavepointReader{}
	blogSavepoints.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{blog}, nil).Once()
	cacheSavepoints.On("ListSavepoints", mock.Anything).Return([]model.Savepoint(nil), nil).Once()
	shopSavepoints.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{shop}, nil).Once()

	// Act
	got, err := readSessionSavepoints(ctx, store, newSavepointsInOrder(t, blogSavepoints, cacheSavepoints, shopSavepoints), demoSession)
	empty, emptyErr := readSessionSavepoints(ctx, store, newSavepointsInOrder(t), model.DefaultSession)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.ComposeProjectSavepoints{
		{ComposeProject: "blog", Savepoints: []model.Savepoint{blog}},
		{ComposeProject: "shop", Savepoints: []model.Savepoint{shop}},
	}, got)
	require.NoError(t, emptyErr)
	assert.Empty(t, empty)
	blogSavepoints.AssertExpectations(t)
	cacheSavepoints.AssertExpectations(t)
	shopSavepoints.AssertExpectations(t)
}

// newSavepointsInOrder opens repos in turn, one per compose project read.
func newSavepointsInOrder(t *testing.T, repos ...*mockSavepointReader) func(storages.Storager) SavepointReader {
	next := 0
	return func(storages.Storager) SavepointReader {
		require.Less(t, next, len(repos), "a compose project read past the repositories given")
		repo := repos[next]
		next++
		return repo
	}
}
