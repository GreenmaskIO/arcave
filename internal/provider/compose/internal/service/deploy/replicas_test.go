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

package deploy

import (
	"context"
	"fmt"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

func TestNewReplicaPairs(t *testing.T) {
	tests := []struct {
		name     string
		recorded []int
		created  []int
		want     []replicaPair
		wantErr  string
	}{
		{
			name: "no gap pairs each replica with itself", recorded: []int{1, 2}, created: []int{1, 2},
			want: []replicaPair{{recorded: 1, created: 1}, {recorded: 2, created: 2}},
		},
		{
			name: "a gap in the middle", recorded: []int{1, 3}, created: []int{1, 2},
			want: []replicaPair{{recorded: 1, created: 1}, {recorded: 3, created: 2}},
		},
		{
			name: "the first replica gone", recorded: []int{2, 3}, created: []int{1, 2},
			want: []replicaPair{{recorded: 2, created: 1}, {recorded: 3, created: 2}},
		},
		{
			name: "a gap at the top, input unsorted", recorded: []int{5, 1, 2}, created: []int{3, 2, 1},
			want: []replicaPair{{recorded: 1, created: 1}, {recorded: 2, created: 2}, {recorded: 5, created: 3}},
		},
		{
			name: "fewer created than recorded", recorded: []int{1, 3}, created: []int{1},
			wantErr: "holds 1 replicas to restore 2 recorded ones into",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := newReplicaPairs(tt.recorded, tt.created)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProvider_rebuildDivergentReplicas(t *testing.T) {
	type rebuild struct {
		created  int
		recorded int
	}
	tests := []struct {
		name     string
		recorded []int
		created  []int
		// sameImage restores every replica on one layer, so only a renumbering
		// makes a rebuild.
		sameImage bool
		want      []rebuild
		wantErr   string
	}{
		{
			name: "no gap rebuilds every replica past the first", recorded: []int{1, 2, 3}, created: []int{1, 2, 3},
			want: []rebuild{{created: 3, recorded: 3}, {created: 2, recorded: 2}},
		},
		{
			name: "a gap in the middle", recorded: []int{1, 3}, created: []int{1, 2},
			want: []rebuild{{created: 2, recorded: 3}},
		},
		{
			name: "the first replica gone renumbers both, from the top down", recorded: []int{2, 3}, created: []int{1, 2},
			want: []rebuild{{created: 2, recorded: 3}, {created: 1, recorded: 2}},
		},
		{
			name: "a gap at the top", recorded: []int{1, 2, 5}, created: []int{1, 2, 3},
			want: []rebuild{{created: 3, recorded: 5}, {created: 2, recorded: 2}},
		},
		{
			name: "one image and a gap still renumbers", recorded: []int{1, 3}, created: []int{1, 2}, sameImage: true,
			want: []rebuild{{created: 2, recorded: 3}},
		},
		{
			name: "fewer created than recorded", recorded: []int{1, 3}, created: []int{1},
			wantErr: "service db: the rebuilt compose project holds 1 replicas to restore 2 recorded ones into",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			replicas := make([]recordedReplica, 0, len(tt.recorded))
			for _, n := range tt.recorded {
				ref := composetest.DemoRestoredTag(n)
				if tt.sameImage {
					ref = composetest.DemoRestoredTag(tt.recorded[0])
				}
				replicas = append(replicas, recordedReplica{
					service: composetest.DemoService, number: n, containerName: composetest.DemoContainerName(n), image: ref,
				})
			}
			built := composetest.DemoIntrospection()
			svc := model.Service{Name: composetest.DemoService}
			for _, n := range tt.created {
				svc.Containers = append(svc.Containers, model.Container{ID: createdID(n), Number: n})
			}
			built.Project.Services = []model.Service{svc}
			inspector := &composetest.MockInspector{}
			inspector.On("Introspect", mock.Anything).Return(built, nil).Once()
			rebuilder := &composetest.MockContainerRebuilder{}
			calls := make([]*mock.Call, 0, len(tt.want))
			for _, w := range tt.want {
				calls = append(calls, rebuilder.On("RecreateWithImage", mock.Anything, createdID(w.created),
					model.ReplicaIdentity{Name: composetest.DemoContainerName(w.recorded), Number: w.recorded},
					imageOf(replicas, w.recorded)).
					Return("rebuilt", nil).Once())
			}
			// A name is claimed only once the container holding it has been rebuilt away.
			mock.InOrder(calls...)
			s := New(Deps{ComposeInspector: inspector, ContainerRebuilder: rebuilder})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := s.rebuildDivergentReplicas(ctx, replicas)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			inspector.AssertExpectations(t)
			rebuilder.AssertExpectations(t)
		})
	}
}

// A compose project compose numbers as recorded, on one image, needs no rebuild and
// not even a look at what was created.
func TestProvider_rebuildDivergentReplicas_CreatedAsRecorded(t *testing.T) {
	// Arrange
	inspector := &composetest.MockInspector{}
	rebuilder := &composetest.MockContainerRebuilder{}
	s := New(Deps{ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	replicas := []recordedReplica{
		{service: composetest.DemoService, number: 2, image: composetest.DemoRestoredTag(1)},
		{service: composetest.DemoService, number: 1, image: composetest.DemoRestoredTag(1)},
	}

	// Act
	err := s.rebuildDivergentReplicas(zerolog.Nop().WithContext(context.Background()), replicas)

	// Assert
	require.NoError(t, err)
	inspector.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
}

func TestProvider_rebuildDivergentReplicas_WarnsOnARenumberedReplica(t *testing.T) {
	// Arrange
	built := composetest.DemoIntrospection()
	built.Project.Services = []model.Service{{Name: composetest.DemoService, Containers: []model.Container{
		{ID: createdID(1), Number: 1}, {ID: createdID(2), Number: 2},
	}}}
	inspector := &composetest.MockInspector{}
	inspector.On("Introspect", mock.Anything).Return(built, nil).Once()
	rebuilder := &composetest.MockContainerRebuilder{}
	rebuilder.On("RecreateWithImage", mock.Anything, createdID(2),
		model.ReplicaIdentity{Name: composetest.DemoContainerName(3), Number: 3}, composetest.DemoRestoredTag(3)).Return("rebuilt", nil).Once()
	s := New(Deps{ComposeInspector: inspector, ContainerRebuilder: rebuilder})
	logs := &composetest.SyncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	err := s.rebuildDivergentReplicas(ctx, []recordedReplica{
		{service: composetest.DemoService, number: 1, containerName: composetest.DemoContainerName(1), image: composetest.DemoRestoredTag(1)},
		{service: composetest.DemoService, number: 3, containerName: composetest.DemoContainerName(3), image: composetest.DemoRestoredTag(3)},
	})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, logs.String(),
		`"service":"db","replica":3,"created_replica":2,"message":"compose numbers replicas without gaps`)
	inspector.AssertExpectations(t)
	rebuilder.AssertExpectations(t)
}

// createdID is the id of the container compose created as replica n.
func createdID(n int) string { return fmt.Sprintf("created-%d", n) }

// imageOf is the image recorded replica n is built on.
func imageOf(replicas []recordedReplica, n int) string {
	for _, replica := range replicas {
		if replica.number == n {
			return replica.image
		}
	}
	return ""
}

// A replica with a restored layer is built on its tag; one the savepoint holds no layer of
// on its service's own image, under the number and name the record keeps for it.
func TestNewRecordedReplicas(t *testing.T) {
	// Arrange
	record := model.Savepoint{NotCovered: &model.NotCovered{WritableLayers: []model.SkippedWritableLayer{
		{Service: "worker", Replica: 1, Container: "demo-worker-1"},
		{Service: "worker", Replica: 3, Container: "demo-worker-3"},
	}}}
	project := &types.Project{Services: types.Services{
		"worker": {Name: "worker", Image: "busybox"},
		"app":    {Name: "app", Image: "app:1"},
	}}
	layered := []model.WritableLayerRestoreResult{{Service: "app", Replica: 2, Tag: "arcave/app:sp-1", ContainerName: "demo-app-2"}}

	// Act
	got := newRecordedReplicas(record, project, layered)

	// Assert
	assert.Equal(t, []recordedReplica{
		{service: "app", number: 2, containerName: "demo-app-2", image: "arcave/app:sp-1"},
		{service: "worker", number: 1, containerName: "demo-worker-1", image: "busybox"},
		{service: "worker", number: 3, containerName: "demo-worker-3", image: "busybox"},
	}, got)
}

// A rollback lets compose recreate only a named volume it restores: a bind or an anonymous
// volume has no config to drift, and a named volume the savepoint left out has no copy.
func TestNewRestoredVolumeNames(t *testing.T) {
	named := model.Volume{ObjectID: "volume-named-demo_db", Named: &model.VolumePropertiesNamed{VolumeName: "demo_db"}}
	bind := model.Volume{ObjectID: "volume-service-app-/src", Bind: &model.VolumePropertiesBind{Service: "app", Target: "/src"}}
	anon := model.Volume{ObjectID: "volume-anon-app-1-/scratch", Anon: &model.VolumePropertiesAnon{Replica: 1}}
	tests := []struct {
		name  string
		stats []model.SnapshotStat
		want  []string
	}{
		{
			name:  "only the named volume, by its docker name",
			stats: []model.SnapshotStat{{Volume: bind}, {Volume: named}, {Volume: anon}},
			want:  []string{"demo_db"},
		},
		{
			name:  "no named volume restored",
			stats: []model.SnapshotStat{{Volume: bind}, {Volume: anon}},
		},
		{
			name: "nothing restored",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newRestoredVolumeNames(model.Savepoint{Stats: tt.stats})

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
