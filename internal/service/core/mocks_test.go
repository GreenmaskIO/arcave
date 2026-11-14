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
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

// mockLocker doubles projectLocker. Release is a closure Acquire returns rather
// than a method of the seam, so it reports itself as one — MethodCalled puts it
// where testify counts it and mock.InOrder can place it.
type mockLocker struct{ mock.Mock }

func (m *mockLocker) Acquire(ctx context.Context) (func() error, error) {
	if err := m.Called(ctx).Error(0); err != nil {
		return nil, err
	}
	return func() error { return m.MethodCalled("Release").Error(0) }, nil
}

// fakeWriteAdmission admits every write, for tests that are not about the admission.
type fakeWriteAdmission struct{}

func (fakeWriteAdmission) AdmitWrite(context.Context) error { return nil }

// mockWriteAdmission doubles writeAdmission.
type mockWriteAdmission struct{ mock.Mock }

func (m *mockWriteAdmission) AdmitWrite(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// fakeSessionLocker always grants the session's lock, and the lock always holds, so tests that
// are not about it carry no expectations.
type fakeSessionLocker struct{}

func (fakeSessionLocker) Lock(
	ctx context.Context, _ model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	return ctx, fakeGrantedSessionLock{}, nil
}

// newFakeSessionLocker opens a fakeSessionLocker for any session.
func newFakeSessionLocker(model.SessionID) (model.SessionLocker, error) {
	return fakeSessionLocker{}, nil
}

// fakeGrantedSessionLock is a session lock that always holds.
type fakeGrantedSessionLock struct{}

func (fakeGrantedSessionLock) Verify(context.Context) error { return nil }
func (fakeGrantedSessionLock) Unlock(context.Context) error { return nil }

// mockSessionLocker doubles model.SessionLocker; Lock hands out the *mockGrantedSessionLock it
// returns, with the caller's context, cancelled with the lock's lostCause when the test sets one.
type mockSessionLocker struct{ mock.Mock }

func (m *mockSessionLocker) Lock(
	ctx context.Context, operation model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	args := m.Called(ctx, operation)
	if err := args.Error(1); err != nil {
		return nil, nil, err
	}
	grantedLock := args.Get(0).(*mockGrantedSessionLock)
	if grantedLock.lostCause == nil {
		return ctx, grantedLock, nil
	}
	lockCtx, cancel := context.WithCancelCause(ctx)
	cancel(grantedLock.lostCause)
	return lockCtx, grantedLock, nil
}

// mockGrantedSessionLock doubles model.GrantedSessionLock; lostCause, when set, is the cause the
// context Lock returned with it is cancelled with.
type mockGrantedSessionLock struct {
	mock.Mock
	lostCause error
}

func (m *mockGrantedSessionLock) Verify(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockGrantedSessionLock) Unlock(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// fakeInflight is a stateful stand-in for the marker file, so tests that are not
// about the gate carry no expectations. Lifecycle tests use mockInflight instead.
type fakeInflight struct {
	mu     sync.Mutex
	marker *model.InflightMarker
}

func (f *fakeInflight) Get(_ context.Context) (model.InflightMarker, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.marker == nil {
		return model.InflightMarker{}, false, nil
	}
	return *f.marker, true, nil
}

func (f *fakeInflight) Put(_ context.Context, marker model.InflightMarker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marker = &marker
	return nil
}

func (f *fakeInflight) Clear(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marker = nil
	return nil
}

// fakePointer is a stateful stand-in for .arcave/current_session; the zero value holds none.
type fakePointer struct {
	mu       sync.Mutex
	id       *model.SessionID
	err      error // returned by every call when set
	clearErr error // returned by Clear alone when set
}

func (f *fakePointer) Get(_ context.Context) (model.SessionID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return model.SessionID{}, false, f.err
	}
	if f.id == nil {
		return model.SessionID{}, false, nil
	}
	return *f.id, true, nil
}

func (f *fakePointer) Put(_ context.Context, id model.SessionID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.id = &id
	return nil
}

func (f *fakePointer) Clear(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.clearErr != nil {
		return f.clearErr
	}
	f.id = nil
	return nil
}

// pointerAt is a fakePointer holding id.
func pointerAt(id model.SessionID) *fakePointer {
	return &fakePointer{id: &id}
}

type mockInflight struct{ mock.Mock }

func (m *mockInflight) Get(ctx context.Context) (model.InflightMarker, bool, error) {
	args := m.Called(ctx)
	marker, _ := args.Get(0).(model.InflightMarker)
	return marker, args.Bool(1), args.Error(2)
}

func (m *mockInflight) Put(ctx context.Context, marker model.InflightMarker) error {
	return m.Called(ctx, marker).Error(0)
}

func (m *mockInflight) Clear(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// syncBuffer is the log sink workflow tests read back; guarded, because the
// fan-out logs from one goroutine per volume.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// mockSession doubles provider.Session.
type mockSession struct {
	mock.Mock
	// id answers ID unmocked.
	id model.SessionID
}

func (m *mockSession) ID() model.SessionID { return m.id }

func (m *mockSession) Workloads(ctx context.Context) ([]provider.WorkloadName, error) {
	args := m.Called(ctx)
	workloads, _ := args.Get(0).([]provider.WorkloadName)
	return workloads, args.Error(1)
}

func (m *mockSession) Savepoints(workload provider.WorkloadName) (provider.Savepoints, error) {
	args := m.Called(workload)
	savepoints, _ := args.Get(0).(provider.Savepoints)
	return savepoints, args.Error(1)
}

func (m *mockSession) FindGarbage(ctx context.Context, dataKinds []model.GarbageDataKind) []provider.Garbage {
	found, _ := m.Called(ctx, dataKinds).Get(0).([]provider.Garbage)
	return found
}

// mockSavepoints doubles provider.Savepoints.
type mockSavepoints struct{ mock.Mock }

func (m *mockSavepoints) List(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *mockSavepoints) Remove(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// mockStages doubles the provider's stages one workflow walks, with two builds: the target's and a
// revert's. Each handle answers the next; AssertExpectations asserts every handle.
type mockStages struct {
	workload mockWorkload
	live     mockLiveWorkload
	plan     mockCapturePlan
	held     mockHeldWorkload
	captured mockCapturedSavepoint
	target   mockBuild
	revert   mockBuild
}

// newMockStages wires the handles to answer one another; absent is what the live workload reports.
func newMockStages(absent bool) *mockStages {
	stages := &mockStages{}
	stages.live.absent = absent
	return stages
}

func (m *mockStages) AssertExpectations(t mock.TestingT) bool {
	results := []bool{
		m.workload.AssertExpectations(t),
		m.live.AssertExpectations(t),
		m.plan.AssertExpectations(t),
		m.held.AssertExpectations(t),
		m.captured.AssertExpectations(t),
		m.target.assertExpectations(t),
		m.revert.assertExpectations(t),
	}
	return !slices.Contains(results, false)
}

// expectInspect declares the workload's inspection, failing with err, else answering the live one.
func (m *mockStages) expectInspect(err error) *mock.Call {
	if err != nil {
		return m.workload.On("Inspect", mock.Anything).Return(nil, err).Once()
	}
	return m.workload.On("Inspect", mock.Anything).Return(&m.live, nil).Once()
}

// expectPlanCapture declares the capture's plan as opts says, failing with err, else answering the
// plan.
func (m *mockStages) expectPlanCapture(opts provider.CaptureOptions, err error) *mock.Call {
	if err != nil {
		return m.live.On("PlanCapture", mock.Anything, opts).Return(nil, err).Once()
	}
	return m.live.On("PlanCapture", mock.Anything, opts).Return(&m.plan, nil).Once()
}

// expectPrepare declares the live workload's preparing of id, failing with err, else answering the
// target's prepared savepoint.
func (m *mockStages) expectPrepare(id model.SavepointID, opts provider.PrepareOptions, err error) *mock.Call {
	if err != nil {
		return m.live.On("PrepareSavepoint", mock.Anything, id, opts).Return(nil, err).Once()
	}
	return m.live.On("PrepareSavepoint", mock.Anything, id, opts).Return(&m.target.prepared, nil).Once()
}

// expectLiveTeardown declares the live workload's teardown of the target, failing with err, else
// answering the target's vacant workload.
func (m *mockStages) expectLiveTeardown(err error) *mock.Call {
	if err != nil {
		return m.live.On("Teardown", mock.Anything, &m.target.prepared).Return(nil, err).Once()
	}
	return m.live.On("Teardown", mock.Anything, &m.target.prepared).Return(&m.target.vacant, nil).Once()
}

// expectHold declares the plan's hold, failing with err, else answering the held workload.
func (m *mockStages) expectHold(err error) *mock.Call {
	if err != nil {
		return m.plan.On("Hold", mock.Anything).Return(nil, err).Once()
	}
	return m.plan.On("Hold", mock.Anything).Return(&m.held, nil).Once()
}

// expectCaptureOf declares the capture of what is held by req, failing with err, else answering
// the captured savepoint.
func (m *mockStages) expectCaptureOf(req provider.CaptureRequest, err error) *mock.Call {
	if err != nil {
		return m.held.On("Capture", mock.Anything, req).Return(nil, err).Once()
	}
	return m.held.On("Capture", mock.Anything, req).Return(&m.captured, nil).Once()
}

// expectCommit declares the commit of what was captured, committing savepoint or failing with err.
func (m *mockStages) expectCommit(savepoint model.Savepoint, err error) *mock.Call {
	return m.captured.On("Commit", mock.Anything).Return(savepoint, err).Once()
}

// expectAbandon declares the abandon of what was captured under id, for a cause wrapping err.
func (m *mockStages) expectAbandon(id model.SavepointID, err error) (named, abandon *mock.Call) {
	named = m.captured.On("ID").Return(id).Once()
	wantCause := mock.MatchedBy(func(cause error) bool { return errors.Is(cause, err) })
	abandon = m.captured.On("Abandon", mock.Anything, wantCause).Once()
	return named, abandon
}

// expectRelease declares the plan's release, waiting waitTimeout, failing with err.
func (m *mockStages) expectRelease(waitTimeout time.Duration, err error) *mock.Call {
	return m.plan.On("Release", mock.Anything, waitTimeout).Return(err).Once()
}

// expectHeldPrepare declares the held workload's preparing of id, failing with err, else
// answering the revert's prepared savepoint.
func (m *mockStages) expectHeldPrepare(id model.SavepointID, opts provider.PrepareOptions, err error) *mock.Call {
	if err != nil {
		return m.held.On("PrepareSavepoint", mock.Anything, id, opts).Return(nil, err).Once()
	}
	return m.held.On("PrepareSavepoint", mock.Anything, id, opts).Return(&m.revert.prepared, nil).Once()
}

// expectHeldTeardown declares the held workload's teardown into build's vacant workload, failing
// with err.
func (m *mockStages) expectHeldTeardown(build *mockBuild, err error) *mock.Call {
	if err != nil {
		return m.held.On("Teardown", mock.Anything).Return(nil, err).Once()
	}
	return m.held.On("Teardown", mock.Anything).Return(&build.vacant, nil).Once()
}

// mockBuild doubles one build's handles: the prepared savepoint, the vacant workload it is built
// on, what Build creates and what RestoreData fills.
type mockBuild struct {
	prepared stubPrepared
	vacant   mockVacantWorkload
	built    mockBuiltWorkload
	restored mockRestoredWorkload
}

func (b *mockBuild) assertExpectations(t mock.TestingT) bool {
	vacant := b.vacant.AssertExpectations(t)
	built := b.built.AssertExpectations(t)
	restored := b.restored.AssertExpectations(t)
	return vacant && built && restored
}

// expectBuild declares the build of the prepared savepoint on the vacant workload, failing with err.
func (b *mockBuild) expectBuild(err error) *mock.Call {
	if err != nil {
		return b.vacant.On("Build", mock.Anything, &b.prepared).Return(nil, err).Once()
	}
	return b.vacant.On("Build", mock.Anything, &b.prepared).Return(&b.built, nil).Once()
}

// expectRestoreData declares the restore of the data, failing with err.
func (b *mockBuild) expectRestoreData(err error) *mock.Call {
	if err != nil {
		return b.built.On("RestoreData", mock.Anything).Return(nil, err).Once()
	}
	return b.built.On("RestoreData", mock.Anything).Return(&b.restored, nil).Once()
}

// expectStart declares the start, waiting waitTimeout, failing with err.
func (b *mockBuild) expectStart(waitTimeout time.Duration, err error) *mock.Call {
	return b.restored.On("Start", mock.Anything, waitTimeout).Return(err).Once()
}

// expectReap declares the reap of what the teardown orphaned.
func (b *mockBuild) expectReap() *mock.Call {
	return b.restored.On("Reap", mock.Anything).Once()
}

// expectDiscard declares the discard of what the build created, failing with err.
func (b *mockBuild) expectDiscard(err error) *mock.Call {
	return b.vacant.On("Discard", mock.Anything).Return(err).Once()
}

// mockWorkload doubles provider.Workload.
type mockWorkload struct{ mock.Mock }

func (m *mockWorkload) Inspect(ctx context.Context) (provider.LiveWorkload, error) {
	args := m.Called(ctx)
	live, _ := args.Get(0).(provider.LiveWorkload)
	return live, args.Error(1)
}

// mockLiveWorkload doubles provider.LiveWorkload; Absent answers absent unmocked.
type mockLiveWorkload struct {
	mock.Mock
	absent bool
}

func (m *mockLiveWorkload) Absent() bool { return m.absent }

func (m *mockLiveWorkload) PlanCapture(ctx context.Context, opts provider.CaptureOptions) (provider.CapturePlan, error) {
	args := m.Called(ctx, opts)
	plan, _ := args.Get(0).(provider.CapturePlan)
	return plan, args.Error(1)
}

func (m *mockLiveWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	args := m.Called(ctx, id, opts)
	prepared, _ := args.Get(0).(provider.PreparedSavepoint)
	return prepared, args.Error(1)
}

func (m *mockLiveWorkload) Teardown(ctx context.Context, prepared provider.PreparedSavepoint) (provider.VacantWorkload, error) {
	args := m.Called(ctx, prepared)
	vacant, _ := args.Get(0).(provider.VacantWorkload)
	return vacant, args.Error(1)
}

// mockCapturePlan doubles provider.CapturePlan; LabelsIgnored answers labelsIgnored unmocked.
type mockCapturePlan struct {
	mock.Mock
	labelsIgnored error
}

func (m *mockCapturePlan) LabelsIgnored() error { return m.labelsIgnored }

func (m *mockCapturePlan) Hold(ctx context.Context) (provider.HeldWorkload, error) {
	args := m.Called(ctx)
	held, _ := args.Get(0).(provider.HeldWorkload)
	return held, args.Error(1)
}

func (m *mockCapturePlan) Release(ctx context.Context, waitTimeout time.Duration) error {
	return m.Called(ctx, waitTimeout).Error(0)
}

// mockHeldWorkload doubles provider.HeldWorkload.
type mockHeldWorkload struct{ mock.Mock }

func (m *mockHeldWorkload) Capture(ctx context.Context, req provider.CaptureRequest) (provider.CapturedSavepoint, error) {
	args := m.Called(ctx, req)
	captured, _ := args.Get(0).(provider.CapturedSavepoint)
	return captured, args.Error(1)
}

func (m *mockHeldWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	args := m.Called(ctx, id, opts)
	prepared, _ := args.Get(0).(provider.PreparedSavepoint)
	return prepared, args.Error(1)
}

func (m *mockHeldWorkload) Teardown(ctx context.Context) (provider.VacantWorkload, error) {
	args := m.Called(ctx)
	vacant, _ := args.Get(0).(provider.VacantWorkload)
	return vacant, args.Error(1)
}

// mockCapturedSavepoint doubles provider.CapturedSavepoint.
type mockCapturedSavepoint struct{ mock.Mock }

func (m *mockCapturedSavepoint) ID() model.SavepointID {
	id, _ := m.Called().Get(0).(model.SavepointID)
	return id
}

func (m *mockCapturedSavepoint) Commit(ctx context.Context) (model.Savepoint, error) {
	args := m.Called(ctx)
	savepoint, _ := args.Get(0).(model.Savepoint)
	return savepoint, args.Error(1)
}

func (m *mockCapturedSavepoint) Abandon(ctx context.Context, cause error) {
	m.Called(ctx, cause)
}

// stubPrepared is a provider.PreparedSavepoint answering summary.
type stubPrepared struct {
	summary provider.PreparedSummary
}

func (s *stubPrepared) Summary() provider.PreparedSummary { return s.summary }

// mockVacantWorkload doubles provider.VacantWorkload.
type mockVacantWorkload struct{ mock.Mock }

func (m *mockVacantWorkload) Build(ctx context.Context, prepared provider.PreparedSavepoint) (provider.BuiltWorkload, error) {
	args := m.Called(ctx, prepared)
	built, _ := args.Get(0).(provider.BuiltWorkload)
	return built, args.Error(1)
}

func (m *mockVacantWorkload) Discard(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// mockBuiltWorkload doubles provider.BuiltWorkload.
type mockBuiltWorkload struct{ mock.Mock }

func (m *mockBuiltWorkload) RestoreData(ctx context.Context) (provider.RestoredWorkload, error) {
	args := m.Called(ctx)
	restored, _ := args.Get(0).(provider.RestoredWorkload)
	return restored, args.Error(1)
}

// mockRestoredWorkload doubles provider.RestoredWorkload.
type mockRestoredWorkload struct{ mock.Mock }

func (m *mockRestoredWorkload) Start(ctx context.Context, waitTimeout time.Duration) error {
	return m.Called(ctx, waitTimeout).Error(0)
}

func (m *mockRestoredWorkload) Reap(ctx context.Context) {
	m.Called(ctx)
}
