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
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// demoWritableLayer is what the backend reports for demoContainerRef's layer.
func demoWritableLayer() composemodel.WritableLayer {
	return composemodel.WritableLayer{
		Service:          composetest.DemoService,
		Replica:          1,
		ContainerID:      composetest.DemoContainerID,
		ContainerName:    composetest.DemoContainerName(1),
		CommittedImageID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImageBackend:     composeconfig.ImageBackendDedupCopy,
		UpstreamImageID:  composetest.DemoUpstreamID,
		UpstreamImageRef: composetest.DemoBaseImage,
		SizeBytes:        4096,
	}
}

// demoImagePlan captures one volume, one image and one writable layer.
func demoImagePlan(backend composemodel.VolumeSnapshotBackend) composemodel.Plan {
	return composemodel.Plan{
		Spec:           demoSpecBytes,
		Volumes:        []composemodel.VolumeSnapshotBackend{backend},
		Images:         []composemodel.ImageCaptureRequest{composetest.DemoImageCaptureRequest()},
		WritableLayers: []composemodel.WritableLayerCaptureRequest{{Container: composetest.DemoContainerRef()}},
		Containers:     []composemodel.ContainerRef{composetest.DemoContainerRef()},
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
		wantNotCovered *composemodel.NotCovered
	}{
		{
			name: "a savepoint",
			req:  provider.CaptureRequest{Name: "sp"},
		},
		{
			name:   "a savepoint taken before another is restored, a container killed",
			killed: []string{composetest.DemoContainerID},
			req: provider.CaptureRequest{
				Labels:      map[string]string{"team": "core"},
				TakenBefore: target,
			},
			wantNotCovered: &composemodel.NotCovered{KilledContainers: []composemodel.KilledContainer{
				{Service: composetest.DemoService, Replica: 1, Container: "demo-db-1"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var id model.SavepointID
			backend := composetest.NewMockBackend(demoVolume())
			stat := composemodel.SnapshotStat{Volume: demoVolume(), Type: composeconfig.SnapshotBackendFullCopy}
			snapshot := backend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
				Run(func(args mock.Arguments) { id = args.Get(1).(composemodel.BackendSnapshotRequest).SnapshotID }).
				Return(stat, nil).Once()
			images := &composetest.MockImageBackend{}
			image := images.On("CaptureImage", mock.Anything, composetest.DemoImageCaptureRequest()).
				Return(demoImage(), nil).Once()
			// The id is read at match time, set by the volume's capture that runs first.
			layer := images.On("CaptureWritableLayer", mock.Anything,
				mock.MatchedBy(func(req composemodel.WritableLayerCaptureRequest) bool {
					return req.SnapshotID == id && req.Container == composetest.DemoContainerRef()
				})).
				Return(demoWritableLayer(), nil).Once()
			repo := &composetest.MockSavepointRepo{}
			spec := repo.On("SaveSpec", mock.Anything,
				mock.MatchedBy(func(specID model.SavepointID) bool { return specID == id }), demoSpecBytes).
				Return(nil).Once()
			var committed composemodel.Savepoint
			commit := repo.On("SaveSavepoint", mock.Anything,
				mock.MatchedBy(func(savepoint composemodel.Savepoint) bool { return savepoint.Header.ID == id })).
				Run(func(args mock.Arguments) { committed = args.Get(1).(composemodel.Savepoint) }).
				Return(nil).Once()
			mock.InOrder(snapshot, image, layer, spec, commit)
			svc := New(Deps{
				SavepointRepo: repo,
				ImageBackend:  images,
			})

			// Act
			savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, demoImagePlan(backend), tt.killed, tt.req)

			// Assert
			require.NoError(t, err)
			want := composemodel.Savepoint{
				Header: model.Savepoint{
					ID:                    id,
					Name:                  tt.req.Name,
					Labels:                tt.req.Labels,
					CreatedAt:             savepoint.Header.CreatedAt,
					TakenBeforeRollbackTo: tt.req.TakenBefore,
				},
				Detail: composemodel.SavepointDetail{
					Stats:          []composemodel.SnapshotStat{stat},
					Images:         []composemodel.Image{demoImage()},
					WritableLayers: []composemodel.WritableLayer{demoWritableLayer()},
					Spec:           &composemodel.SpecRef{Format: composemodel.SpecFormatComposeYAML},
					NotCovered:     tt.wantNotCovered,
				},
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
			wantErr:  "capture image " + string(composetest.DemoUpstreamID) + ": daemon refused",
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
			backend := composetest.NewMockBackend(demoVolume())
			backend.On("Snapshot", mock.Anything, composetest.MatchValidSnapshotID()).
				Run(func(args mock.Arguments) { id = args.Get(1).(composemodel.BackendSnapshotRequest).SnapshotID }).
				Return(composemodel.SnapshotStat{Volume: demoVolume(), Type: composeconfig.SnapshotBackendFullCopy}, nil).Once()
			images := &composetest.MockImageBackend{}
			image := images.On("CaptureImage", mock.Anything, composetest.DemoImageCaptureRequest()).
				Return(demoImage(), tt.imageErr).Once()
			if tt.imageErr == nil {
				layer := images.On("CaptureWritableLayer", mock.Anything,
					mock.MatchedBy(func(req composemodel.WritableLayerCaptureRequest) bool { return req.SnapshotID == id })).
					Return(composemodel.WritableLayer{}, tt.layerErr).Once()
				mock.InOrder(image, layer)
			}
			repo := &composetest.MockSavepointRepo{}
			svc := New(Deps{
				SavepointRepo: repo,
				ImageBackend:  images,
			})

			// Act
			savepoint, err := captureAndCommit(composetest.NewTestContext(), svc, demoImagePlan(backend), nil, provider.CaptureRequest{Name: "sp"})

			// Assert
			require.ErrorIs(t, err, errBoom)
			assert.EqualError(t, err, tt.wantErr)
			assert.Zero(t, savepoint)
			backend.AssertExpectations(t)
			images.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

// An image the backend could not store is a warning the result repeats, never a failure.
func TestCapturer_Capture_ImageNotStoredWarns(t *testing.T) {
	// Arrange
	notStored := composetest.DemoImageCaptureRequest().Image
	images := &composetest.MockImageBackend{}
	images.On("CaptureImage", mock.Anything, composetest.DemoImageCaptureRequest()).Return(notStored, nil).Once()
	repo := &composetest.MockSavepointRepo{}
	repo.On("SaveSpec", mock.Anything, mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil }), demoSpecBytes).
		Return(nil).Once()
	repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint composemodel.Savepoint) bool {
		return assert.ObjectsAreEqual([]composemodel.Image{notStored}, savepoint.Detail.Images)
	})).Return(nil).Once()
	svc := New(Deps{
		SavepointRepo: repo,
		ImageBackend:  images,
	})
	plan := composemodel.Plan{
		Spec:   demoSpecBytes,
		Images: []composemodel.ImageCaptureRequest{composetest.DemoImageCaptureRequest()},
	}
	recorder := &composetest.WarnedRecorder{}
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, now))

	// Act
	savepoint, err := captureAndCommit(ctx, svc, plan, nil, provider.CaptureRequest{Name: "sp", InResult: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []composemodel.Image{notStored}, savepoint.Detail.Images)
	assert.Equal(t, []string{"not stored: a replica left out of the capture cannot be rebuilt from it"}, recorder.Msgs)
	assert.Equal(t, []bool{true}, recorder.InResult)
	images.AssertExpectations(t)
	repo.AssertExpectations(t)
}
