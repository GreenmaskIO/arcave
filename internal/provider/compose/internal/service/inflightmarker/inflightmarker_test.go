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

package inflightmarker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	testArcaveProject  = model.ArcaveProjectName("demo-arcave")
	testComposeProject = model.DockerComposeProjectName("demo")
)

var (
	errDaemon = errors.New("daemon unreachable")
	testAt    = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
)

// newTestMarker is the marker of a rollback of compose project demo at phase, written at at.
func newTestMarker(phase model.InflightPhase, at time.Time) model.InflightMarker {
	return model.InflightMarker{
		Phase:          phase,
		Session:        model.DefaultSession,
		ComposeProject: testComposeProject,
		Target:         "019e843e-1650-7000-8000-000000000001",
		Safety:         "019e843e-1650-7000-8000-000000000002",
		At:             at,
	}
}

// newMarkerVolume is a marker volume named name holding marker.
func newMarkerVolume(name string, marker model.InflightMarker) volume.Volume {
	return volume.Volume{
		Name: name,
		Labels: inflightMarkerVolume{
			ArcaveProject: testArcaveProject,
			Marker:        marker,
		}.Labels(),
	}
}

type mockDockerAPI struct{ mock.Mock }

// VolumeList answers a client.VolumeListResult, or what a func() client.VolumeListResult returns
// when it is called.
func (m *mockDockerAPI) VolumeList(ctx context.Context, options client.VolumeListOptions) (client.VolumeListResult, error) {
	args := m.Called(ctx, options)
	if listed, ok := args.Get(0).(func() client.VolumeListResult); ok {
		return listed(), args.Error(1)
	}
	result, _ := args.Get(0).(client.VolumeListResult)
	return result, args.Error(1)
}

func (m *mockDockerAPI) VolumeCreate(ctx context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	args := m.Called(ctx, options)
	result, _ := args.Get(0).(client.VolumeCreateResult)
	return result, args.Error(1)
}

func (m *mockDockerAPI) VolumeRemove(ctx context.Context, volumeID string, options client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	args := m.Called(ctx, volumeID, options)
	return client.VolumeRemoveResult{}, args.Error(0)
}

// arcaveProjectListing is the listing of testArcaveProject's marker volumes, a read's.
var arcaveProjectListing = client.VolumeListOptions{
	Filters: make(client.Filters).Add("label", "io.arcave.inflight.arcave-project="+testArcaveProject.String()),
}

// composeProjectListing is the listing of the marker volumes testComposeProject wrote, a write's.
var composeProjectListing = client.VolumeListOptions{
	Filters: make(client.Filters).Add("label",
		"io.arcave.inflight.arcave-project="+testArcaveProject.String(),
		"io.arcave.inflight.compose-project="+testComposeProject.String()),
}

// newCreateMatcher matches the creation of marker's volume: a local one of testComposeProject,
// named apart from every other write, its labels the marker whole.
func newCreateMatcher(marker model.InflightMarker) any {
	return mock.MatchedBy(func(options client.VolumeCreateOptions) bool {
		return strings.HasPrefix(options.Name, "arcave-inflight-demo-") &&
			options.Name != "arcave-inflight-demo-old" &&
			options.Driver == "local" &&
			assert.ObjectsAreEqual(inflightMarkerVolume{
				ArcaveProject: testArcaveProject,
				Marker:        marker,
			}.Labels(), options.Labels)
	})
}

func TestStore_Get(t *testing.T) {
	older := newTestMarker(model.InflightRecreating, testAt)
	newer := newTestMarker(model.InflightRestoring, testAt.Add(time.Second))
	sameTime := newTestMarker(model.InflightRestoreDone, testAt)
	broken := newMarkerVolume("arcave-inflight-demo-broken", older)
	delete(broken.Labels, labelPhase.String())

	tests := []struct {
		name       string
		volumes    []volume.Volume
		listErr    error
		wantMarker model.InflightMarker
		wantFound  bool
		wantErr    string
	}{
		{name: "none stands"},
		{
			name:       "one stands",
			volumes:    []volume.Volume{newMarkerVolume("arcave-inflight-demo-a", older)},
			wantMarker: older,
			wantFound:  true,
		},
		{
			name: "the newest by time, listed first",
			volumes: []volume.Volume{
				newMarkerVolume("arcave-inflight-demo-b", newer),
				newMarkerVolume("arcave-inflight-demo-c", older),
			},
			wantMarker: newer,
			wantFound:  true,
		},
		{
			name: "the newest by time, listed last",
			volumes: []volume.Volume{
				newMarkerVolume("arcave-inflight-demo-c", older),
				newMarkerVolume("arcave-inflight-demo-b", newer),
			},
			wantMarker: newer,
			wantFound:  true,
		},
		{
			name: "the later name at one time, listed first",
			volumes: []volume.Volume{
				newMarkerVolume("arcave-inflight-demo-b", sameTime),
				newMarkerVolume("arcave-inflight-demo-a", older),
			},
			wantMarker: sameTime,
			wantFound:  true,
		},
		{
			name: "the later name at one time, listed last",
			volumes: []volume.Volume{
				newMarkerVolume("arcave-inflight-demo-a", older),
				newMarkerVolume("arcave-inflight-demo-b", sameTime),
			},
			wantMarker: sameTime,
			wantFound:  true,
		},
		{
			name:    "a volume missing a label",
			volumes: []volume.Volume{broken},
			wantErr: "arcave-inflight-demo-broken",
		},
		{name: "the listing fails", listErr: errDaemon, wantErr: errDaemon.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockDockerAPI{}
			api.On("VolumeList", mock.Anything, arcaveProjectListing).
				Return(client.VolumeListResult{Items: tt.volumes}, tt.listErr).Once()
			store := NewStore(api, testArcaveProject, testComposeProject)

			// Act
			marker, found, err := store.Get(context.Background())

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantMarker, marker)
			assert.Equal(t, tt.wantFound, found)
			api.AssertExpectations(t)
		})
	}
}

// A write creates its volume first, then removes the compose project's earlier ones; what fails
// after the new volume exists is logged, never the write's error.
func TestStore_Put(t *testing.T) {
	marker := newTestMarker(model.InflightRestoring, testAt)
	earlier := []volume.Volume{
		newMarkerVolume("arcave-inflight-demo-old", newTestMarker(model.InflightRecreating, testAt)),
	}

	tests := []struct {
		name      string
		createErr error
		listed    []volume.Volume
		listErr   error
		removeErr error
		wantErr   error
	}{
		{name: "the earlier volumes go", listed: earlier},
		{name: "none before it"},
		{name: "the creation fails", createErr: errDaemon, wantErr: errDaemon},
		{name: "the listing after it fails", listErr: errDaemon},
		{name: "removing an earlier volume fails", listed: earlier, removeErr: errDaemon},
		{name: "an earlier volume is already gone", listed: earlier, removeErr: cerrdefs.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockDockerAPI{}
			calls := []*mock.Call{
				api.On("VolumeCreate", mock.Anything, newCreateMatcher(marker)).
					Return(client.VolumeCreateResult{}, tt.createErr).Once(),
			}
			if tt.createErr == nil {
				calls = append(calls, api.On("VolumeList", mock.Anything, composeProjectListing).
					Return(client.VolumeListResult{Items: tt.listed}, tt.listErr).Once())
			}
			for _, v := range tt.listed {
				calls = append(calls, api.On("VolumeRemove", mock.Anything, v.Name, client.VolumeRemoveOptions{}).
					Return(tt.removeErr).Once())
			}
			mock.InOrder(calls...)
			store := NewStore(api, testArcaveProject, testComposeProject)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := store.Put(ctx, marker)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			api.AssertExpectations(t)
		})
	}
}

// The volume a write lists back as its own is not removed.
func TestStore_Put_KeepsItsOwnVolume(t *testing.T) {
	// Arrange
	marker := newTestMarker(model.InflightRestoreDone, testAt)
	var created string
	api := &mockDockerAPI{}
	api.On("VolumeCreate", mock.Anything, newCreateMatcher(marker)).
		Run(func(args mock.Arguments) { created = args.Get(1).(client.VolumeCreateOptions).Name }).
		Return(client.VolumeCreateResult{}, nil).Once()
	api.On("VolumeList", mock.Anything, composeProjectListing).
		Return(func() client.VolumeListResult {
			return client.VolumeListResult{Items: []volume.Volume{newMarkerVolume(created, marker)}}
		}, nil).Once()
	store := NewStore(api, testArcaveProject, testComposeProject)

	// Act
	err := store.Put(context.Background(), marker)

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
}

// Clear removes what the compose project wrote, the listing scoped to it.
func TestStore_Clear(t *testing.T) {
	tests := []struct {
		name      string
		volumes   []string
		listErr   error
		removeErr error
		wantErr   error
	}{
		{name: "none stands"},
		{name: "every volume goes", volumes: []string{"arcave-inflight-demo-a", "arcave-inflight-demo-b"}},
		{name: "one already gone", volumes: []string{"arcave-inflight-demo-a"}, removeErr: cerrdefs.ErrNotFound},
		{name: "a removal fails", volumes: []string{"arcave-inflight-demo-a"}, removeErr: errDaemon, wantErr: errDaemon},
		{name: "the listing fails", listErr: errDaemon, wantErr: errDaemon},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockDockerAPI{}
			items := make([]volume.Volume, 0, len(tt.volumes))
			for _, name := range tt.volumes {
				items = append(items, volume.Volume{Name: name})
				api.On("VolumeRemove", mock.Anything, name, client.VolumeRemoveOptions{}).Return(tt.removeErr).Once()
			}
			api.On("VolumeList", mock.Anything, composeProjectListing).
				Return(client.VolumeListResult{Items: items}, tt.listErr).Once()
			store := NewStore(api, testArcaveProject, testComposeProject)

			// Act
			err := store.Clear(context.Background())

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			api.AssertExpectations(t)
		})
	}
}

// A marker of a compose project the store does not write is refused before anything is created.
func TestStore_Put_AnotherComposeProject(t *testing.T) {
	// Arrange: the strict mock expects no call.
	marker := newTestMarker(model.InflightRestoring, testAt)
	marker.ComposeProject = "other"
	api := &mockDockerAPI{}
	store := NewStore(api, testArcaveProject, testComposeProject)

	// Act
	err := store.Put(context.Background(), marker)

	// Assert
	require.ErrorIs(t, err, errAnotherComposeProject)
	api.AssertExpectations(t)
}
