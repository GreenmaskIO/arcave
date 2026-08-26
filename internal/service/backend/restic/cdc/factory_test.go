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

package cdc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestFactory_Kind(t *testing.T) {
	assert.Equal(t, model.SnapshotBackendCDC, NewFactory(Dependencies{}).Kind())
}

// The repository is the volume's own directory, one segment under the
// repository root, and it is resolved at construction — unlike the volume's
// address, which needs a ctx the constructor does not have. Whichever shape the
// ObjectID has, it stays one segment: it is also the name on the wire.
func TestFactory_New(t *testing.T) {
	tests := []struct {
		name        string
		volume      model.Volume
		wantMounted string
	}{
		{
			name:        "a named volume",
			volume:      demoVolume(),
			wantMounted: "/m/" + demoObjectID,
		},
		{
			name: "a bind mount, whose target is flattened into the one segment",
			volume: model.Volume{
				ObjectID:       "volume-service-app-etc-cfg",
				ComposeProject: demoProject,
				Type:           model.VolumeTypeBind,
				Bind:           &model.VolumePropertiesBind{Service: "app", Target: "/etc/cfg"},
			},
			wantMounted: "/m/volume-service-app-etc-cfg",
		},
		{
			name: "a named volume no service mounts is built like any other",
			volume: model.Volume{
				ObjectID: "volume-named-orphan",
				Type:     model.VolumeTypeNamed,
				Named:    &model.VolumePropertiesNamed{VolumeName: "orphan"},
			},
			wantMounted: "/m/volume-named-orphan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			root := newStore(t)
			factory := NewFactory(Dependencies{
				RepoURLBase:    demoRepoURLBase,
				RepositoryRoot: root,
				Options:        defaultOptions(),
			})

			// Act
			backend, err := factory.New(model.BackendSpec{Volume: tt.volume})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.volume, backend.Volume())
			snapshotter, ok := backend.(*Snapshotter)
			require.True(t, ok)
			// One directory per volume, directly under the root — the very tree the
			// server resolves a name against, so the two halves cannot diverge.
			assert.Equal(t, root.GetCwd()+"/"+tt.volume.ObjectID, snapshotter.repo.store.GetCwd())
			assert.Equal(t, demoRepoURLBase+"/"+tt.volume.ObjectID+"/", snapshotter.repositoryURL())
			assert.Equal(t, tt.wantMounted, snapshotter.mounted)
		})
	}
}

// spec.Store is deliberately unread: a savepoint and a rollback's undo of one
// volume address the same repository, so restic can pick a parent snapshot and
// the undo costs an increment rather than a full backup.
func TestFactory_New_IgnoresTheSpecStore(t *testing.T) {
	// Arrange
	root := newStore(t)
	factory := NewFactory(Dependencies{RepositoryRoot: root, Options: defaultOptions()})

	// Act
	savepoint, err := factory.New(model.BackendSpec{Volume: demoVolume(), Store: newStore(t)})
	require.NoError(t, err)
	safety, err := factory.New(model.BackendSpec{Volume: demoVolume(), Store: newStore(t)})
	require.NoError(t, err)

	// Assert
	assert.Equal(t,
		savepoint.(*Snapshotter).repo.store.GetCwd(),
		safety.(*Snapshotter).repo.store.GetCwd())
}
