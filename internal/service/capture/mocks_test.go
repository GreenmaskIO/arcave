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

package capture

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

const demoVolumeName = "pgdata"

var (
	demoSpec      = &types.Project{Name: "demo"}
	demoSpecBytes = []byte("name: demo\n")
)

// demoVolume is the unit, in the form the mock tests address it by.
func demoVolume() model.Volume {
	return model.Volume{
		ObjectID: "volume-named-" + demoVolumeName,
		Backend:  model.SnapshotBackendFullCopy,
		Named:    &model.VolumePropertiesNamed{VolumeName: demoVolumeName},
	}
}

type mockSavepointRepo struct{ mock.Mock }

func (m *mockSavepointRepo) SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error {
	return m.Called(ctx, id, spec).Error(0)
}

func (m *mockSavepointRepo) SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error {
	return m.Called(ctx, savepoint).Error(0)
}

// expectSpecCommit declares the artifact a capture writes before its record, under an id
// off the clock that passes the gate.
func expectSpecCommit(repo *mockSavepointRepo) *mock.Call {
	validID := mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil })
	return repo.On("SaveSpec", mock.Anything, validID, demoSpecBytes).Return(nil).Once()
}

type mockComposeRunner struct{ mock.Mock }

func (m *mockComposeRunner) Stop(ctx context.Context, project *types.Project) ([]string, error) {
	args := m.Called(ctx, project)
	killed, _ := args.Get(0).([]string)
	return killed, args.Error(1)
}

func (m *mockComposeRunner) Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	return m.Called(ctx, project, waitTimeout).Error(0)
}

// mockBackend doubles model.VolumeSnapshotBackend; Volume() answers volume unmocked.
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

// matchValidSnapshotID matches a Snapshot request whose id passes the gate; the
// id comes off the clock, so no literal can name it.
func matchValidSnapshotID() any {
	return mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil
	})
}

type mockImageBackend struct{ mock.Mock }

func (m *mockImageBackend) CaptureImage(ctx context.Context, req model.ImageCaptureRequest) (model.Image, error) {
	args := m.Called(ctx, req)
	image, _ := args.Get(0).(model.Image)
	return image, args.Error(1)
}

func (m *mockImageBackend) CaptureWritableLayer(
	ctx context.Context, req model.WritableLayerCaptureRequest,
) (model.WritableLayer, error) {
	args := m.Called(ctx, req)
	layer, _ := args.Get(0).(model.WritableLayer)
	return layer, args.Error(1)
}

// fakeGrantedSessionLock is a session lock that always holds.
type fakeGrantedSessionLock struct{}

func (fakeGrantedSessionLock) Verify(context.Context) error { return nil }
func (fakeGrantedSessionLock) Unlock(context.Context) error { return nil }

// mockGrantedSessionLock doubles model.GrantedSessionLock.
type mockGrantedSessionLock struct{ mock.Mock }

func (m *mockGrantedSessionLock) Verify(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockGrantedSessionLock) Unlock(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// mockCaptureTrigger doubles captureTrigger.
type mockCaptureTrigger struct{ mock.Mock }

func (m *mockCaptureTrigger) beforeCapture(ctx context.Context) ([]string, error) {
	args := m.Called(ctx)
	killed, _ := args.Get(0).([]string)
	return killed, args.Error(1)
}

func (m *mockCaptureTrigger) afterCapture(revertCtx context.Context) error {
	return m.Called(revertCtx).Error(0)
}

// syncBuffer is the log sink tests read back; guarded, because the fan-out logs from one
// goroutine per unit.
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

// warnedRecorder keeps every warning, and whether it was marked as the result repeats it.
type warnedRecorder struct {
	msgs     []string
	inResult []bool
}

func (r *warnedRecorder) Render(evt progress.Event) {
	if evt.Status == progress.StatusWarned {
		r.msgs = append(r.msgs, evt.Msg)
		r.inResult = append(r.inResult, evt.InResult)
	}
}

// newDirStore is a real directory Storager on a tempdir.
func newDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// objectNames lists a store's whole tree.
func objectNames(t *testing.T, store storages.Storager) []string {
	t.Helper()

	written, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(written))
	for _, obj := range written {
		names = append(names, obj.Name)
	}
	return names
}

// readCaptureHeartbeat reads the heartbeat of id's tree.
func readCaptureHeartbeat(t *testing.T, store storages.Storager, id model.SavepointID) heartbeat.Heartbeat {
	t.Helper()
	tree, err := store.SubStorage(id.String(), true)
	require.NoError(t, err)
	beat, err := heartbeat.NewReader(tree).Read(context.Background())
	require.NoError(t, err)
	return beat
}
