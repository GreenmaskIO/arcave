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

package compose

import (
	"fmt"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/imagebackend"
	"github.com/greenmaskio/arcave/internal/service/imagebackend/dedupcopy"
	imagefullcopy "github.com/greenmaskio/arcave/internal/service/imagebackend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/fullcopy"
	"github.com/greenmaskio/arcave/internal/utils"
)

// newBackendRegistry builds every volume backend: a rollback restores through the backend its
// record names, so cdc is wired, its section's defaults included, whatever snapshot.backend says.
func newBackendRegistry(
	cfg config.Config, resticRoot storages.Storager,
	streamer fullcopy.VolumeStreamer, runner cdc.VolumeRunner, transport cdc.Transport,
) (model.SnapshotBackendRegistry, error) {
	backendRegistry, err := NewBackendRegistry(
		fullcopy.NewFactory(streamer, fullcopy.Compression(cfg.Backends.FullCopy.Compression)),
		cdc.NewFactory(cdc.Dependencies{
			Runner:         runner,
			Transport:      transport,
			RepositoryRoot: resticRoot,
			Options:        newCDCOptions(config.CDCWithDefaults(cfg.Backends.CDC)),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build snapshot backend registry: %w", err)
	}
	return backendRegistry, nil
}

// newImageBackend builds the image backend over both archivers a writable layer may name;
// images are always deduplicated, which needs a daemon of Docker 25 or newer.
func newImageBackend(
	composeProjectStore storages.Navigator, origin model.LayerOrigin, engine *docker.Engine,
) (*imagebackend.Copier, error) {
	blobStore, err := composeProjectStore.SubStorage(dedupcopy.NamespaceBlobs, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at the shared blob store: %w", err)
	}
	dedup := dedupcopy.New(blobStore)
	return imagebackend.NewCopier(composeProjectStore, origin, engine, map[model.ImageBackendKind]imagebackend.ImageArchiver{
		model.ImageBackendDedupCopy: dedup,
		model.ImageBackendFullCopy:  imagefullcopy.New(),
	}, dedup), nil
}

// newCaptureDefaults is what the config says of a unit no arcave.* label names. The
// capture keys are read as ApplyDefaults left them: every one set.
func newCaptureDefaults(cfg config.Config) capturepolicy.CaptureDefaults {
	return capturepolicy.CaptureDefaults{
		VolumeBackend:        cfg.Snapshot.BackendKind(),
		WritableLayerBackend: cfg.WritableLayers.BackendKind(),
		Mode:                 model.CaptureMode(cfg.Capture.Mode),
		SavepointCapture: capturepolicy.SavepointCaptureSettings{
			WritableLayers:   utils.Value(cfg.Capture.WritableLayers),
			Images:           utils.Value(cfg.Capture.Images),
			NamedVolumes:     utils.Value(cfg.Capture.Volumes.Named),
			BindVolumes:      utils.Value(cfg.Capture.Volumes.Bind),
			AnonymousVolumes: utils.Value(cfg.Capture.Volumes.Anonymous),
		},
	}
}

// newSavepointRepository is the savepoint repository of a compose project's store, as the provider
// reads it with no daemon.
func newSavepointRepository(store storages.Storager) SavepointRepository {
	return sprepo.New(store)
}
