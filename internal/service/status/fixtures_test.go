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

package status

import (
	"fmt"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// demoArcaveProject is the arcave project the deployment labels name.
const demoArcaveProject model.ArcaveProjectName = "demo-arcave"

const (
	demoProject    = "demo"
	demoService    = "db"
	demoVolumeName = "demo_db"
)

// demoVolume is a named volume of demoService.
func demoVolume() model.Volume {
	return model.Volume{
		ObjectID: "volume-named-" + demoVolumeName,
		Backend:  model.SnapshotBackendFullCopy,
		Named:    &model.VolumePropertiesNamed{VolumeName: demoVolumeName},
	}
}

// demoWritableLayer is the writable layer of demoService's one replica.
func demoWritableLayer() model.WritableLayer {
	return model.WritableLayer{
		Service:          demoService,
		Replica:          1,
		ContainerID:      "c0ffee",
		ContainerName:    fmt.Sprintf("%s-%s-%d", demoProject, demoService, 1),
		CommittedImageID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImageBackend:     model.ImageBackendDedupCopy,
		UpstreamImageID:  "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		UpstreamImageRef: "postgres:16",
		SizeBytes:        4096,
	}
}

func newDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}
