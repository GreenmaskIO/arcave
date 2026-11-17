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
	"cmp"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// captureAndCommit captures plan as held, killed being what the hold killed, and commits it; a
// request with no id is given a fresh one.
func captureAndCommit(
	ctx context.Context, svc *Capturer, plan composemodel.Plan, killed []string, req provider.CaptureRequest,
) (model.Savepoint, error) {
	req.ID = cmp.Or(req.ID, model.NewSavepointID())
	captured, err := svc.Capture(ctx, plan, killed, req)
	if err != nil {
		return model.Savepoint{}, err
	}
	return captured.Commit(ctx)
}

// Commit writes the record; a Commit whose write fails returns the zero savepoint.
func TestCaptured_Commit(t *testing.T) {
	tests := []struct {
		name      string
		commitErr error
		wantErr   string
	}{
		{
			name: "a commit lands the record",
		},
		{
			name:      "a failed commit writes no record",
			commitErr: errors.New("store unreachable"),
			wantErr:   "store unreachable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			id := model.NewSavepointID()
			backend := composetest.NewMockBackend(demoVolume())
			snapshot := backend.On("Snapshot", mock.Anything, mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
				return req.SnapshotID == id
			})).Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
			repo := &composetest.MockSavepointRepo{}
			commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
				return savepoint.ID == id && savepoint.Name == "sp"
			})).Return(tt.commitErr).Once()
			mock.InOrder(snapshot, expectSpecCommit(repo), commit)
			svc := New(Deps{SavepointRepo: repo})
			plan := composemodel.Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}
			ctx := composetest.NewTestContext()
			captured, err := svc.Capture(ctx, plan, nil, provider.CaptureRequest{ID: id, Name: "sp"})
			require.NoError(t, err)

			// Act
			savepoint, err := captured.Commit(ctx)

			// Assert
			if tt.wantErr != "" {
				assert.EqualError(t, err, "commit savepoint "+id.String()+": "+tt.wantErr)
				assert.Zero(t, savepoint)
			} else {
				require.NoError(t, err)
				assert.Equal(t, id, savepoint.ID)
			}
			backend.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

// The record takes its name, description, labels and rollback target from the request, and
// lands after the spec.
func TestCapturer_Capture_RecordsTheRequest(t *testing.T) {
	// Arrange
	req := provider.CaptureRequest{
		ID:          model.NewSavepointID(),
		Name:        "sp",
		Description: "before the migration",
		Labels:      map[string]string{"team": "core"},
		TakenBefore: &model.SavepointRef{ID: "019e843e-1650-7000-8000-000000000000", Name: "v1"},
	}
	repo := &composetest.MockSavepointRepo{}
	spec := expectSpecCommit(repo)
	commit := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID == req.ID
	})).Return(nil).Once()
	mock.InOrder(spec, commit)
	svc := New(Deps{SavepointRepo: repo})

	// Act
	savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, composemodel.Plan{Spec: demoSpecBytes}, nil, req)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, req.ID, savepoint.ID)
	assert.Equal(t, req.Name, savepoint.Name)
	assert.Equal(t, req.Description, savepoint.Description)
	assert.Equal(t, req.Labels, savepoint.Labels)
	assert.Equal(t, req.TakenBefore, savepoint.TakenBeforeRollbackTo)
	assert.Equal(t, &model.SpecRef{Format: model.SpecFormatComposeYAML}, savepoint.Spec)
	repo.AssertExpectations(t)
}

// A volume that fails commits nothing: the savepoint stays invisible rather than list with
// half its volumes.
func TestCapturer_Capture_UncommittedOnFailure(t *testing.T) {
	// Arrange
	backend := composetest.NewMockBackend(demoVolume())
	backend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
		Return(model.SnapshotStat{}, errors.New("capture boom")).Once()
	repo := &composetest.MockSavepointRepo{}
	svc := New(Deps{SavepointRepo: repo})
	plan := composemodel.Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

	// Act
	savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, plan, nil, provider.CaptureRequest{Name: "sp"})

	// Assert
	require.EqualError(t, err, "snapshot volume-named-demo_db: capture volume: capture boom")
	assert.Zero(t, savepoint)
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// What a half-finished fan-out wrote stays for a later gc: no record names it, and a failure path
// never deletes data.
func TestCapturer_Capture_RetainsWhatAPartialCaptureWrote(t *testing.T) {
	// Arrange
	store := composetest.NewDirStore(t)
	id := model.NewSavepointID()
	written := composetest.NewMockBackend(demoVolume())
	written.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
		Run(func(args mock.Arguments) {
			key := id.String() + "/" + demoVolume().ObjectID + "/data.tar"
			require.NoError(t, store.PutObject(context.Background(), key, strings.NewReader("pgdata bytes")))
		}).
		Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
	failed := composetest.NewMockBackend(model.Volume{ObjectID: "volume-service-app-etc-cfg"})
	failed.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
		Return(model.SnapshotStat{}, errors.New("capture boom")).Once()
	repo := &composetest.MockSavepointRepo{}
	// One at a time, so the failure comes after the first volume is written.
	svc := New(Deps{SavepointRepo: repo, Concurrency: 1})
	plan := composemodel.Plan{Volumes: []model.VolumeSnapshotBackend{written, failed}, Spec: demoSpecBytes}

	// Act
	savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, plan, nil,
		provider.CaptureRequest{ID: id, Name: "sp"})

	// Assert
	require.ErrorContains(t, err, "capture boom")
	assert.Zero(t, savepoint)
	assert.Equal(t, []string{id.String() + "/" + demoVolume().ObjectID + "/data.tar"},
		composetest.ObjectNames(t, store), "the archive the surviving volume wrote is kept")
	written.AssertExpectations(t)
	failed.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// The id is the request's: every backend writes under it, and the record is committed under it.
func TestCapturer_Capture_WritesUnderTheRequestedID(t *testing.T) {
	// Arrange
	id := model.NewSavepointID()
	var volumeID, layerID, specID model.SavepointID
	backend := composetest.NewMockBackend(demoVolume())
	backend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
		Run(func(args mock.Arguments) { volumeID = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
		Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
	images := &composetest.MockImageBackend{}
	images.On("CaptureWritableLayer", mock.Anything, mock.MatchedBy(func(req composemodel.WritableLayerCaptureRequest) bool {
		return req.Container == demoContainerRef()
	})).
		Run(func(args mock.Arguments) { layerID = args.Get(1).(composemodel.WritableLayerCaptureRequest).SnapshotID }).
		Return(demoWritableLayer(), nil).Once()
	repo := &composetest.MockSavepointRepo{}
	repo.On("SaveSpec", mock.Anything, mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil }), demoSpecBytes).
		Run(func(args mock.Arguments) { specID = args.Get(1).(model.SavepointID) }).
		Return(nil).Once()
	repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return savepoint.ID == id
	})).Return(nil).Once()
	svc := New(Deps{SavepointRepo: repo, ImageBackend: images})
	plan := composemodel.Plan{
		Spec:           demoSpecBytes,
		Volumes:        []model.VolumeSnapshotBackend{backend},
		WritableLayers: []composemodel.WritableLayerCaptureRequest{{Container: demoContainerRef()}},
	}

	// Act
	savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, plan, nil,
		provider.CaptureRequest{ID: id, Name: "sp"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, id, savepoint.ID)
	assert.WithinDuration(t, time.Now(), savepoint.CreatedAt, time.Minute)
	assert.Equal(t, id, volumeID)
	assert.Equal(t, id, layerID)
	assert.Equal(t, id, specID)
	backend.AssertExpectations(t)
	images.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// An id that is not a savepoint's is refused before anything is written under it.
func TestCapturer_Capture_RefusesAnInvalidID(t *testing.T) {
	// Arrange
	backend := composetest.NewMockBackend(demoVolume())
	repo := &composetest.MockSavepointRepo{}
	svc := New(Deps{SavepointRepo: repo})
	plan := composemodel.Plan{Volumes: []model.VolumeSnapshotBackend{backend}, Spec: demoSpecBytes}

	// Act
	captured, err := svc.Capture(composetest.NewTestContext(), plan, nil, provider.CaptureRequest{ID: "..", Name: "sp"})

	// Assert
	require.ErrorIs(t, err, model.ErrInvalidSavepointID)
	assert.Nil(t, captured)
	backend.AssertExpectations(t)
	repo.AssertExpectations(t)
}
