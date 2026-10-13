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
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockSavepointRepo struct{ mock.Mock }

func (m *mockSavepointRepo) ListSavepoints(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	records, _ := args.Get(0).([]model.Savepoint)
	return records, args.Error(1)
}

func (m *mockSavepointRepo) SaveSavepoint(ctx context.Context, record model.Savepoint) error {
	return m.Called(ctx, record).Error(0)
}

func (m *mockSavepointRepo) SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error {
	return m.Called(ctx, id, spec).Error(0)
}

func (m *mockSavepointRepo) LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error) {
	args := m.Called(ctx, id)
	spec, _ := args.Get(0).([]byte)
	return spec, args.Error(1)
}

func (m *mockSavepointRepo) DeleteSavepoint(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// mockImageBackend doubles imageBackend.
type mockImageBackend struct{ mock.Mock }

func (m *mockImageBackend) Capture(
	ctx context.Context, req model.ImageCaptureRequest,
) (model.ImageCapture, error) {
	args := m.Called(ctx, req)
	capture, _ := args.Get(0).(model.ImageCapture)
	return capture, args.Error(1)
}

func (m *mockImageBackend) CaptureUpstream(
	ctx context.Context, req model.UpstreamImageCaptureRequest,
) (model.UpstreamImage, error) {
	args := m.Called(ctx, req)
	image, _ := args.Get(0).(model.UpstreamImage)
	return image, args.Error(1)
}

func (m *mockImageBackend) Restore(
	ctx context.Context, req model.ImageRestoreRequest,
) (model.ImageRestore, error) {
	args := m.Called(ctx, req)
	restored, _ := args.Get(0).(model.ImageRestore)
	return restored, args.Error(1)
}

// mockContainerRebuilder doubles containerRebuilder.
type mockContainerRebuilder struct{ mock.Mock }

func (m *mockContainerRebuilder) RecreateWithImage(
	ctx context.Context, containerID string, as model.ReplicaIdentity, imageRef string,
) (string, error) {
	args := m.Called(ctx, containerID, as, imageRef)
	return args.String(0), args.Error(1)
}

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

type mockInspector struct{ mock.Mock }

func (m *mockInspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	args := m.Called(ctx)
	return args.Get(0).(model.IntrospectionResult), args.Error(1)
}

type mockAssembler struct{ mock.Mock }

func (m *mockAssembler) Assemble(ctx context.Context, res model.IntrospectionResult) (*types.Project, error) {
	args := m.Called(ctx, res)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}

type mockCodec struct{ mock.Mock }

func (m *mockCodec) Marshal(project *types.Project) ([]byte, error) {
	args := m.Called(project)
	spec, _ := args.Get(0).([]byte)
	return spec, args.Error(1)
}

func (m *mockCodec) Parse(ctx context.Context, spec []byte) (*types.Project, error) {
	args := m.Called(ctx, spec)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}

type mockComposeRunner struct{ mock.Mock }

func (m *mockComposeRunner) Stop(ctx context.Context, project *types.Project) ([]string, error) {
	args := m.Called(ctx, project)
	killed, _ := args.Get(0).([]string)
	return killed, args.Error(1)
}

func (m *mockComposeRunner) Down(ctx context.Context, project *types.Project) error {
	return m.Called(ctx, project).Error(0)
}

func (m *mockComposeRunner) Create(ctx context.Context, project *types.Project) error {
	return m.Called(ctx, project).Error(0)
}

func (m *mockComposeRunner) Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	return m.Called(ctx, project, waitTimeout).Error(0)
}

type mockVolumeRemover struct{ mock.Mock }

func (m *mockVolumeRemover) RemoveVolume(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

type mockRegistry struct{ mock.Mock }

func (m *mockRegistry) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	args := m.Called(spec)
	backend, _ := args.Get(0).(model.VolumeSnapshotBackend)
	return backend, args.Error(1)
}

// mockBackend doubles model.VolumeSnapshotBackend; volume answers the Volume()
// accessor directly — it is identity, not behavior a test expects on.
type mockBackend struct {
	mock.Mock
	volume model.Volume
}

func (m *mockBackend) Volume() model.Volume { return m.volume }

func (m *mockBackend) Snapshot(ctx context.Context, req model.BackendSnapshotRequest) (model.SnapshotStat, error) {
	args := m.Called(ctx, req)
	stat, _ := args.Get(0).(model.SnapshotStat)
	return stat, args.Error(1)
}

func (m *mockBackend) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	return m.Called(ctx, req).Error(0)
}

func (m *mockBackend) Remove(ctx context.Context, req model.BackendRemoveRequest) error {
	return m.Called(ctx, req).Error(0)
}

// matchSpec matches a registry New's BackendSpec argument by the root it names
// and the volume it is for. There is one root now, so the store no longer tells
// a savepoint's instances from a rollback's undo — it is asserted because every
// backend must be bound to the store the DI root handed down, and to no other.
func matchSpec(store storages.Storager, objectID string) any {
	return mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Store == store && spec.Volume.ObjectID == objectID
	})
}

// matchValidSnapshotID matches a Snapshot request whose id passes the gate; the
// id comes off the clock, so no literal can name it.
func matchValidSnapshotID() any {
	return mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil
	})
}

// matchRetargetedImage matches the project a rebuild is handed by the image one of
// its services is pointed at; the pull policy rides along, since a tag that lives
// only in the local daemon must never be resolved remotely.
func matchRetargetedImage(service, ref string) any {
	return mock.MatchedBy(func(project *types.Project) bool {
		svc, ok := project.Services[service]
		return ok && svc.Image == ref && svc.PullPolicy == types.PullPolicyNever
	})
}

// matchSafetySnapshot matches a safety snapshot's capture request: an id off the
// clock that passes the gate, carrying the label a rollback stamps. That label
// is the only thing marking the capture as an undo — the record it commits is an
// ordinary savepoint's — so it is matched rather than widened away.
func matchSafetySnapshot() any {
	return mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil &&
			req.Labels[model.SafetySnapshotLabel] == model.SafetySnapshotLabelValue
	})
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
