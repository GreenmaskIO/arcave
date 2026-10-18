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

package test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/docker/client"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	imagefullcopy "github.com/greenmaskio/arcave/internal/service/backend/imagecopy/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// layerMarker is written into the container's own filesystem, past every mount:
// state no volume capture can see.
const layerMarker = "/layer-marker"

// imageCaptureSpec is one service writing to its writable layer and one volume,
// so a savepoint has both halves to take.
const imageCaptureSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sh", "-c", "printf captured > %[2]s; sleep 3600"]
    stop_signal: SIGKILL
    volumes:
      - data:/data
volumes:
  data: {}
`

func TestSavepoint_CommitsTheWritableLayer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageCaptureEnv(t)
	env.up(t)

	// Act
	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "with-layer"})

	// Assert
	require.NoError(t, err)
	require.Len(t, savepoint.WritableLayers, 1, "one container, one capture")
	capture := savepoint.WritableLayers[0]
	assert.Equal(t, "app", capture.Service)
	assert.Equal(t, 1, capture.Replica)
	assert.NotEmpty(t, capture.CommittedImageID)
	assert.Equal(t, "busybox", capture.UpstreamImageRef)
	assert.NotEmpty(t, capture.UpstreamImageID)
	assert.Positive(t, capture.SizeBytes)

	// The capture's own manifest, and the metadata the archive carried.
	// Written out rather than taken from imagecopy: the key is the compatibility
	// contract, and a test sharing the constant would not notice it change.
	captureDir := filepath.Join(savepoint.ID.String(), "images", "app", "replicas", "1")
	keys := storeKeys(t, env.store)
	for _, name := range []string{"image.json", "index.json", "manifest.json", "oci-layout"} {
		assert.Contains(t, keys, filepath.Join(captureDir, name))
	}
	assert.Less(t, capture.StoredBytes, capture.SizeBytes,
		"the image is kept first, so the writable layer stores only what it adds over it")

	var shared int
	for _, key := range keys {
		if strings.HasPrefix(key, filepath.Join("shared", "oci", "blobs")+string(filepath.Separator)) {
			shared++
		}
	}
	assert.Positive(t, shared, "the archive's blobs went to the shared store")

	// The upstream is kept once, by content, beside the blobs it shares with the layer.
	require.Len(t, savepoint.Images, 1)
	upstream := savepoint.Images[0]
	assert.Equal(t, capture.UpstreamImageID, upstream.ID)
	assert.Contains(t, upstream.Refs, "busybox")
	assert.True(t, upstream.Stored)
	upstreamDir := filepath.Join("shared", "oci", "images", "sha256",
		strings.TrimPrefix(upstream.ID.String(), "sha256:"))
	assert.Contains(t, keys, filepath.Join(upstreamDir, "image.json"))

	// The committed image is gone from the daemon: the store is the only copy.
	_, err = env.daemon.ImageInspect(env.ctx, capture.CommittedImageID.String())
	assert.True(t, cerrdefs.IsNotFound(err), "the committed image outlived the capture: %v", err)
}

// imageCaptureEnv is the compose project this test drives, wired the way cmd/arcave wires it.
type imageCaptureEnv struct {
	ctx     context.Context
	svc     *core.Service
	compose *composeclient.Client
	daemon  *client.Client
	store   storages.Storager
	project string
}

func newImageCaptureEnv(t *testing.T) *imageCaptureEnv {
	t.Helper()

	daemonAPI := newDaemonClient(t)
	project := "arcave-image-it-" + randomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	composeService := dockercompose.NewComposeService(cli)
	engine := dockerclient.NewEngine(daemonAPI)
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second,
	})

	projectRoot := t.TempDir()
	store, err := storefactory.New(t.Context(), config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, "arcave-image-tenant", model.DefaultSession, model.DockerComposeProjectName(project))
	require.NoError(t, err)

	blobStore, err := store.SubStorage(dedupcopy.BlobPrefix, true)
	require.NoError(t, err)

	svc := core.New(core.Deps{
		SavepointRepo:      sprepo.New(store),
		ProjectLocker:      lock.New(projectRoot),
		ComposeInspector:   introspect.NewInspector(daemonAPI, model.DockerComposeProjectName(project)),
		ProjectAssembler:   composespec.NewAssembler(),
		SpecCodec:          composespec.NewCodec(),
		ComposeRunner:      runner,
		VolumeRemover:      engine,
		ImageBackend:       newImageCopier(store, engine, blobStore),
		ContainerRebuilder: engine,
		ImageLookup:        engine,
		BackendRegistry:    mustRegistry(t, engine),
		Inflight:           inflightrepo.New(projectRoot),
		Store:              store,
		CaptureDefaults:    newCaptureDefaults(model.SnapshotBackendFullCopy),
	})

	env := &imageCaptureEnv{
		ctx:     zerolog.New(io.Discard).WithContext(context.Background()),
		svc:     svc,
		compose: runner,
		daemon:  daemonAPI,
		store:   store,
		project: project,
	}
	t.Cleanup(func() { removeComposeProject(t, env.daemon, env.project) })
	return env
}

func (e *imageCaptureEnv) up(t *testing.T) {
	t.Helper()

	document := fmt.Sprintf(imageCaptureSpec, e.project, layerMarker)
	project, err := composespec.NewCodec().Parse(e.ctx, []byte(document))
	require.NoError(t, err)
	require.NoError(t, e.compose.Create(e.ctx, project))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
}

// storeKeys lists the store's whole tree.
func storeKeys(t *testing.T, store storages.Storager) []string {
	t.Helper()

	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Name)
	}
	return keys
}

// newImageCopier is the image backend the DI root builds: both writable layer archivers, and
// images deduplicated.
func newImageCopier(store storages.Navigator, engine *dockerclient.Engine, blobStore storages.Storager) *imagecopy.Copier {
	dedup := dedupcopy.New(blobStore)
	return imagecopy.NewCopier(store, engine, map[model.ImageBackendKind]imagecopy.ImageArchiver{
		model.ImageBackendDedupCopy: dedup,
		model.ImageBackendFullCopy:  imagefullcopy.New(),
	}, dedup)
}

// newCaptureDefaults are the capture defaults the DI root builds from a config that names
// only the volume backend: every kind captured, layers deduplicated.
func newCaptureDefaults(volumeBackend model.SnapshotBackendKind) capturepolicy.CaptureDefaults {
	return capturepolicy.CaptureDefaults{
		VolumeBackend:        volumeBackend,
		WritableLayerBackend: model.ImageBackendDedupCopy,
		SavepointCapture: capturepolicy.SavepointCaptureSettings{
			WritableLayers:   true,
			Images:           true,
			NamedVolumes:     true,
			BindVolumes:      true,
			AnonymousVolumes: true,
		},
	}
}
