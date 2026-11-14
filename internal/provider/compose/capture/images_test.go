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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/captureplan"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// demoWritableLayer is what the backend reports for demoContainerRef's layer.
func demoWritableLayer() model.WritableLayer {
	return model.WritableLayer{
		Service:          demoService,
		Replica:          1,
		ContainerID:      demoContainerID,
		ContainerName:    demoContainerName(1),
		CommittedImageID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImageBackend:     model.ImageBackendDedupCopy,
		UpstreamImageID:  demoUpstreamID,
		UpstreamImageRef: demoBaseImage,
		SizeBytes:        4096,
	}
}

// demoImagePlan captures one volume, one image and one writable layer.
func demoImagePlan(backend model.VolumeSnapshotBackend) captureplan.Plan {
	return captureplan.Plan{
		Spec:           demoSpecBytes,
		Volumes:        []model.VolumeSnapshotBackend{backend},
		Images:         []model.ImageCaptureRequest{demoImageCaptureRequest()},
		WritableLayers: []model.WritableLayerCaptureRequest{{Container: demoContainerRef()}},
		Containers:     []model.ContainerRef{demoContainerRef()},
	}
}

// The images are captured after the volumes and before the layers, every one under the
// savepoint's id, and the record carries both and the request's attributes as given.
func TestCapturer_Capture_CommitsTheImagesAndWritableLayers(t *testing.T) {
	target := &model.SavepointRef{ID: "019e843e-1650-7000-8000-000000000000", Name: "before-migration"}
	tests := []struct {
		name           string
		killed         []string
		req            provider.CaptureRequest
		wantNotCovered *model.NotCovered
	}{
		{
			name: "a savepoint",
			req:  provider.CaptureRequest{Name: "sp"},
		},
		{
			name:   "a savepoint taken before another is restored, a container killed",
			killed: []string{demoContainerID},
			req: provider.CaptureRequest{
				Labels:      map[string]string{"team": "core"},
				TakenBefore: target,
			},
			wantNotCovered: &model.NotCovered{KilledContainers: []model.KilledContainer{
				{Service: demoService, Replica: 1, Container: "demo-db-1"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var id model.SavepointID
			backend := composetest.NewMockBackend(demoVolume())
			stat := model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}
			snapshot := backend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
				Run(func(args mock.Arguments) { id = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
				Return(stat, nil).Once()
			images := &composetest.MockImageBackend{}
			image := images.On("CaptureImage", mock.Anything, demoImageCaptureRequest()).
				Return(demoImage(), nil).Once()
			// The id is read at match time, set by the volume's capture that runs first.
			layer := images.On("CaptureWritableLayer", mock.Anything,
				mock.MatchedBy(func(req model.WritableLayerCaptureRequest) bool {
					return req.SnapshotID == id && req.Container == demoContainerRef()
				})).
				Return(demoWritableLayer(), nil).Once()
			repo := &composetest.MockSavepointRepo{}
			spec := repo.On("SaveSpec", mock.Anything,
				mock.MatchedBy(func(specID model.SavepointID) bool { return specID == id }), demoSpecBytes).
				Return(nil).Once()
			var committed model.Savepoint
			commit := repo.On("SaveSavepoint", mock.Anything,
				mock.MatchedBy(func(savepoint model.Savepoint) bool { return savepoint.ID == id })).
				Run(func(args mock.Arguments) { committed = args.Get(1).(model.Savepoint) }).
				Return(nil).Once()
			mock.InOrder(snapshot, image, layer, spec, commit)
			svc := New(Deps{
				SavepointRepo: repo,
				ImageBackend:  images,
				Store:         composetest.NewDirStore(t),
			})

			// Act
			savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, demoImagePlan(backend), tt.killed, tt.req)

			// Assert
			require.NoError(t, err)
			want := model.Savepoint{
				ID:                    id,
				Name:                  tt.req.Name,
				Labels:                tt.req.Labels,
				CreatedAt:             savepoint.CreatedAt,
				TakenBeforeRollbackTo: tt.req.TakenBefore,
				Stats:                 []model.SnapshotStat{stat},
				Images:                []model.Image{demoImage()},
				WritableLayers:        []model.WritableLayer{demoWritableLayer()},
				Spec:                  &model.SpecRef{Format: model.SpecFormatComposeYAML},
				NotCovered:            tt.wantNotCovered,
			}
			assert.Equal(t, want, savepoint)
			assert.Equal(t, want, committed, "the record is what the caller gets")
			backend.AssertExpectations(t)
			images.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

// An image or a layer that fails commits nothing: neither the spec nor the record.
func TestCapturer_Capture_FailedImageCaptureCommitsNothing(t *testing.T) {
	errBoom := errors.New("daemon refused")
	tests := []struct {
		name     string
		imageErr error
		layerErr error
		wantErr  string
	}{
		{
			name:     "an image",
			imageErr: errBoom,
			wantErr:  "capture image " + string(demoUpstreamID) + ": daemon refused",
		},
		{
			name:     "a writable layer",
			layerErr: errBoom,
			wantErr:  "capture writable layer of db/1: daemon refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var id model.SavepointID
			store := composetest.NewDirStore(t)
			backend := composetest.NewMockBackend(demoVolume())
			backend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
				Run(func(args mock.Arguments) { id = args.Get(1).(model.BackendSnapshotRequest).SnapshotID }).
				Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, nil).Once()
			images := &composetest.MockImageBackend{}
			image := images.On("CaptureImage", mock.Anything, demoImageCaptureRequest()).
				Return(demoImage(), tt.imageErr).Once()
			if tt.imageErr == nil {
				layer := images.On("CaptureWritableLayer", mock.Anything,
					mock.MatchedBy(func(req model.WritableLayerCaptureRequest) bool { return req.SnapshotID == id })).
					Return(model.WritableLayer{}, tt.layerErr).Once()
				mock.InOrder(image, layer)
			}
			repo := &composetest.MockSavepointRepo{}
			svc := New(Deps{
				SavepointRepo: repo,
				ImageBackend:  images,
				Store:         store,
			})

			// Act
			savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, demoImagePlan(backend), nil, provider.CaptureRequest{Name: "sp"})

			// Assert
			require.ErrorIs(t, err, errBoom)
			assert.EqualError(t, err, tt.wantErr)
			assert.Zero(t, savepoint)
			assert.Equal(t, heartbeat.StatusFailed, composetest.ReadCaptureHeartbeat(t, store, id).Status)
			backend.AssertExpectations(t)
			images.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

// An image the backend could not store is a warning the result repeats, never a failure.
func TestCapturer_Capture_ImageNotStoredWarns(t *testing.T) {
	// Arrange
	notStored := demoImageCaptureRequest().Image
	images := &composetest.MockImageBackend{}
	images.On("CaptureImage", mock.Anything, demoImageCaptureRequest()).Return(notStored, nil).Once()
	repo := &composetest.MockSavepointRepo{}
	repo.On("SaveSpec", mock.Anything, mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil }), demoSpecBytes).
		Return(nil).Once()
	repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
		return assert.ObjectsAreEqual([]model.Image{notStored}, savepoint.Images)
	})).Return(nil).Once()
	svc := New(Deps{
		SavepointRepo: repo,
		ImageBackend:  images,
		Store:         composetest.NewDirStore(t),
	})
	plan := captureplan.Plan{
		Spec:   demoSpecBytes,
		Images: []model.ImageCaptureRequest{demoImageCaptureRequest()},
	}
	recorder := &composetest.WarnedRecorder{}
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, now))

	// Act
	savepoint, err := captureAndCommit(ctx, svc, plan, nil, provider.CaptureRequest{Name: "sp", InResult: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Image{notStored}, savepoint.Images)
	assert.Equal(t, []string{"not stored: a replica left out of the capture cannot be rebuilt from it"}, recorder.Msgs)
	assert.Equal(t, []bool{true}, recorder.InResult)
	images.AssertExpectations(t)
	repo.AssertExpectations(t)
}
