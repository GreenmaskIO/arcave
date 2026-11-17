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

// Package composetest holds the doubles and fixtures the compose provider's packages share in their
// tests. Every mock forwards all of its seam's arguments to Called, ctx included; tests match ctx
// with mock.Anything unless they assert on it.
package composetest

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// MockSavepointRepo doubles the savepoint repository every package of the provider reads through.
type MockSavepointRepo struct{ mock.Mock }

func (m *MockSavepointRepo) SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error {
	return m.Called(ctx, id, spec).Error(0)
}

func (m *MockSavepointRepo) SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error {
	return m.Called(ctx, savepoint).Error(0)
}

func (m *MockSavepointRepo) LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error) {
	args := m.Called(ctx, ids)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *MockSavepointRepo) LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error) {
	args := m.Called(ctx, id)
	spec, _ := args.Get(0).([]byte)
	return spec, args.Error(1)
}

func (m *MockSavepointRepo) ListSavepoints(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *MockSavepointRepo) DeleteSavepoint(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// MockComposeRunner doubles the compose project's lifecycle.
type MockComposeRunner struct{ mock.Mock }

func (m *MockComposeRunner) Stop(ctx context.Context, project *types.Project) ([]string, error) {
	args := m.Called(ctx, project)
	killed, _ := args.Get(0).([]string)
	return killed, args.Error(1)
}

func (m *MockComposeRunner) StopServices(ctx context.Context, project *types.Project, services []string) ([]string, error) {
	args := m.Called(ctx, project, services)
	killed, _ := args.Get(0).([]string)
	return killed, args.Error(1)
}

func (m *MockComposeRunner) Down(ctx context.Context, project *types.Project) error {
	return m.Called(ctx, project).Error(0)
}

func (m *MockComposeRunner) Create(ctx context.Context, project *types.Project, restoredVolumes []string) error {
	return m.Called(ctx, project, restoredVolumes).Error(0)
}

func (m *MockComposeRunner) Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	return m.Called(ctx, project, waitTimeout).Error(0)
}

// MockContainerControl doubles the pause, the unpause and the wait on health probes.
type MockContainerControl struct{ mock.Mock }

func (m *MockContainerControl) PauseContainers(ctx context.Context, ids []string) error {
	return m.Called(ctx, ids).Error(0)
}

func (m *MockContainerControl) UnpauseContainers(ctx context.Context, ids []string) error {
	return m.Called(ctx, ids).Error(0)
}

func (m *MockContainerControl) AwaitHealthProbes(ctx context.Context, ids []string) error {
	return m.Called(ctx, ids).Error(0)
}

// MockImageBackend doubles the image backend, both ways.
type MockImageBackend struct{ mock.Mock }

func (m *MockImageBackend) CaptureImage(ctx context.Context, req model.ImageCaptureRequest) (model.Image, error) {
	args := m.Called(ctx, req)
	image, _ := args.Get(0).(model.Image)
	return image, args.Error(1)
}

func (m *MockImageBackend) CaptureWritableLayer(
	ctx context.Context, req model.WritableLayerCaptureRequest,
) (model.WritableLayer, error) {
	args := m.Called(ctx, req)
	layer, _ := args.Get(0).(model.WritableLayer)
	return layer, args.Error(1)
}

func (m *MockImageBackend) RestoreWritableLayer(
	ctx context.Context, req model.WritableLayerRestoreRequest,
) (model.WritableLayerRestoreResult, error) {
	args := m.Called(ctx, req)
	result, _ := args.Get(0).(model.WritableLayerRestoreResult)
	return result, args.Error(1)
}

// MockBackend doubles model.VolumeSnapshotBackend; Volume() answers its volume unmocked.
type MockBackend struct {
	mock.Mock
	volume model.Volume
}

// NewMockBackend is a backend of volume.
func NewMockBackend(volume model.Volume) *MockBackend {
	return &MockBackend{volume: volume}
}

func (m *MockBackend) Volume() model.Volume { return m.volume }

func (m *MockBackend) Snapshot(ctx context.Context, req model.BackendSnapshotRequest) (model.SnapshotStat, error) {
	args := m.Called(ctx, req)
	stat, _ := args.Get(0).(model.SnapshotStat)
	return stat, args.Error(1)
}

func (m *MockBackend) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	return m.Called(ctx, req).Error(0)
}

// MatchValidSnapshotID matches a Snapshot request whose id passes the gate; the id comes off the
// clock, so no literal can name it.
func MatchValidSnapshotID() any {
	return mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil
	})
}

// FakeVolumeStreamer is a volume streamer with nothing to stream: a factory needs one to build.
type FakeVolumeStreamer struct{}

func (FakeVolumeStreamer) ResolveMountSource(_ context.Context, v model.Volume) (model.MountSource, error) {
	return model.MountSource{Type: model.MountSourceVolume, Source: v.ObjectID}, nil
}

func (FakeVolumeStreamer) OpenVolume(context.Context, model.MountSource) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (FakeVolumeStreamer) WriteVolume(_ context.Context, _ model.MountSource, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// SyncBuffer is the log sink tests read back; guarded, because the fan-out logs from one
// goroutine per unit.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// WarnedRecorder keeps every warning, and whether it was marked as the result repeats it.
type WarnedRecorder struct {
	Msgs     []string
	InResult []bool
}

func (r *WarnedRecorder) Render(evt progress.Event) {
	if evt.Status == progress.StatusWarned {
		r.Msgs = append(r.Msgs, evt.Msg)
		r.InResult = append(r.InResult, evt.InResult)
	}
}

// StepRecorder keeps the kind of every step opened and every warning.
type StepRecorder struct {
	Kinds    []progress.Kind
	Warnings []string
}

func (r *StepRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		r.Kinds = append(r.Kinds, evt.Open.Step.Kind)
	}
	if evt.Status == progress.StatusWarned {
		r.Warnings = append(r.Warnings, evt.Msg)
	}
}

// NewTestContext is a context logging nowhere.
func NewTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
}

// NewProgressContext is a context under a root step reporting to recorder.
func NewProgressContext(recorder progress.Renderer) context.Context {
	ctx := zerolog.Nop().WithContext(context.Background())
	ctx, _ = progress.Begin(progress.WithTracker(ctx, progress.New(recorder, time.Now)), progress.Step{Kind: "test"})
	return ctx
}

// NewDirStore is a real directory Storager on a tempdir.
func NewDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// ObjectNames lists a store's whole tree.
func ObjectNames(t *testing.T, store storages.Storager) []string {
	t.Helper()
	written, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(written))
	for _, obj := range written {
		names = append(names, obj.Name)
	}
	return names
}

// ReadCaptureHeartbeat reads the heartbeat of id's tree.
func ReadCaptureHeartbeat(t *testing.T, store storages.Storager, id model.SavepointID) heartbeat.Heartbeat {
	t.Helper()
	tree, err := store.SubStorage(id.String(), true)
	require.NoError(t, err)
	beat, err := heartbeat.NewReader(tree).Read(context.Background())
	require.NoError(t, err)
	return beat
}
