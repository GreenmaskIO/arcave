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
	"slices"
	"sync"
	"time"

	"github.com/stretchr/testify/assert"
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

// mockSessionLookup doubles sessionLookup.
type mockSessionLookup struct{ mock.Mock }

func (m *mockSessionLookup) LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(model.Session), args.Bool(1), args.Error(2)
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

// mockSavepoints doubles provider.Savepoints.
type mockSavepoints struct{ mock.Mock }

func (m *mockSavepoints) List(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *mockSavepoints) Load(ctx context.Context, id model.SavepointID) (model.Savepoint, bool, error) {
	args := m.Called(ctx, id)
	savepoint, _ := args.Get(0).(model.Savepoint)
	return savepoint, args.Bool(1), args.Error(2)
}

func (m *mockSavepoints) Remove(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// mockStages doubles the provider's stages one workflow walks, with two builds: the target's and a
// revert's. Each handle answers the next; AssertExpectations asserts every handle.
type mockStages struct {
	workload mockWorkload
	live     mockLiveWorkload
	surveyed mockSurveyedWorkload
	dropped  mockDroppedWorkload
	plan     mockCapturePlan
	held     mockHeldWorkload
	captured mockCapturedSavepoint
	target   mockBuild
	revert   mockBuild
	// capturedID is the id core minted for the last capture.
	capturedID model.SavepointID
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
		m.surveyed.AssertExpectations(t),
		m.dropped.AssertExpectations(t),
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

// expectSurvey declares the workload's survey of resources, failing with err, else answering the
// surveyed one.
func (m *mockStages) expectSurvey(resources model.DiscardedResources, present bool, err error) *mock.Call {
	if err != nil {
		return m.workload.On("Survey", mock.Anything).Return(nil, err).Once()
	}
	m.surveyed.resources, m.surveyed.present = resources, present
	return m.workload.On("Survey", mock.Anything).Return(&m.surveyed, nil).Once()
}

// expectSurveyedPrepare declares the surveyed workload's preparing of id, failing with err, else
// answering the target's prepared savepoint.
func (m *mockStages) expectSurveyedPrepare(id model.SavepointID, opts provider.PrepareOptions, err error) *mock.Call {
	if err != nil {
		return m.surveyed.On("PrepareSavepoint", mock.Anything, id, opts).Return(nil, err).Once()
	}
	return m.surveyed.On("PrepareSavepoint", mock.Anything, id, opts).Return(&m.target.prepared, nil).Once()
}

// expectOwner declares the surveyed workload's judging of its owner, answering deployment and err.
func (m *mockStages) expectOwner(deployment *model.SavepointDeployment, err error) *mock.Call {
	return m.surveyed.On("Owner", demoArcaveProject, model.DefaultSession).Return(deployment, err).Once()
}

// expectDrop declares the surveyed workload's drop, failing with err, else answering the dropped one.
func (m *mockStages) expectDrop(err error) *mock.Call {
	if err != nil {
		return m.surveyed.On("Drop", mock.Anything).Return(nil, err).Once()
	}
	return m.surveyed.On("Drop", mock.Anything).Return(&m.dropped, nil).Once()
}

// expectVacant declares the dropped workload's vacant one for the target, the target's.
func (m *mockStages) expectVacant() *mock.Call {
	return m.dropped.On("Vacant", &m.target.prepared).Return(&m.target.vacant, nil).Once()
}

// expectRemoveRestoredLayers declares the dropped workload's untag, answering removed and notices.
func (m *mockStages) expectRemoveRestoredLayers(removed []model.ImageRef, notices []model.Notice) *mock.Call {
	return m.dropped.On("RemoveRestoredLayers", mock.Anything).Return(removed, notices).Once()
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
// the captured savepoint. A req with no id matches any valid one, kept as capturedID.
func (m *mockStages) expectCaptureOf(req provider.CaptureRequest, err error) *mock.Call {
	matchReq := mock.MatchedBy(func(got provider.CaptureRequest) bool {
		want := req
		if want.ID == "" && got.ID.Validate() == nil {
			want.ID = got.ID
		}
		return assert.ObjectsAreEqual(want, got)
	})
	var captured provider.CapturedSavepoint
	if err == nil {
		captured = &m.captured
	}
	return m.held.On("Capture", mock.Anything, matchReq).
		Run(func(args mock.Arguments) { m.capturedID = args.Get(1).(provider.CaptureRequest).ID }).
		Return(captured, err).Once()
}

// expectCommit declares the commit of what was captured, committing savepoint or failing with err.
func (m *mockStages) expectCommit(savepoint model.Savepoint, err error) *mock.Call {
	return m.captured.On("Commit", mock.Anything).Return(savepoint, err).Once()
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

func (m *mockWorkload) Survey(ctx context.Context) (provider.SurveyedWorkload, error) {
	args := m.Called(ctx)
	surveyed, _ := args.Get(0).(provider.SurveyedWorkload)
	return surveyed, args.Error(1)
}

// mockSurveyedWorkload doubles provider.SurveyedWorkload; Resources and Present answer resources
// and present unmocked.
type mockSurveyedWorkload struct {
	mock.Mock
	resources model.DiscardedResources
	present   bool
}

func (m *mockSurveyedWorkload) Resources() model.DiscardedResources { return m.resources }

func (m *mockSurveyedWorkload) Present() bool { return m.present }

func (m *mockSurveyedWorkload) Owner(
	arcaveProject model.ArcaveProjectName, session model.SessionID,
) (*model.SavepointDeployment, error) {
	args := m.Called(arcaveProject, session)
	deployment, _ := args.Get(0).(*model.SavepointDeployment)
	return deployment, args.Error(1)
}

func (m *mockSurveyedWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	args := m.Called(ctx, id, opts)
	prepared, _ := args.Get(0).(provider.PreparedSavepoint)
	return prepared, args.Error(1)
}

func (m *mockSurveyedWorkload) Drop(ctx context.Context) (provider.DroppedWorkload, error) {
	args := m.Called(ctx)
	dropped, _ := args.Get(0).(provider.DroppedWorkload)
	return dropped, args.Error(1)
}

// mockDroppedWorkload doubles provider.DroppedWorkload; Notices answers notices unmocked.
type mockDroppedWorkload struct {
	mock.Mock
	notices []model.Notice
}

func (m *mockDroppedWorkload) Notices() []model.Notice { return m.notices }

func (m *mockDroppedWorkload) Vacant(prepared provider.PreparedSavepoint) (provider.VacantWorkload, error) {
	args := m.Called(prepared)
	vacant, _ := args.Get(0).(provider.VacantWorkload)
	return vacant, args.Error(1)
}

func (m *mockDroppedWorkload) RemoveRestoredLayers(ctx context.Context) ([]model.ImageRef, []model.Notice) {
	args := m.Called(ctx)
	removed, _ := args.Get(0).([]model.ImageRef)
	notices, _ := args.Get(1).([]model.Notice)
	return removed, notices
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

func (m *mockCapturedSavepoint) Commit(ctx context.Context) (model.Savepoint, error) {
	args := m.Called(ctx)
	savepoint, _ := args.Get(0).(model.Savepoint)
	return savepoint, args.Error(1)
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
