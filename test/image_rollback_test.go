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
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/greenmaskio/storages"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose"
	"github.com/greenmaskio/arcave/internal/provider/compose/sessionstore"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// volumeMarker sits in the named volume, layerMarker past every mount: a rollback
// has to put both back, and only the second comes out of an image.
const volumeMarker = "/data/marker"

// imageRollbackSpec is one service scaled by the test, writing to its own writable
// layer and to a volume the replicas share.
const imageRollbackSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: %[2]d
    volumes:
      - data:/data
volumes:
  data: {}
`

func TestRollback_RestoresTheWritableLayer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.up(t, 1)
	env.write(t, env.replicas(t)[0], "original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "with-layer"})
	require.NoError(t, err)
	require.Len(t, savepoint.WritableLayers, 1)

	// The layer drifts, and so does the volume beside it.
	env.write(t, env.replicas(t)[0], "drifted")

	// Act
	res, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	require.Len(t, res.WritableLayers, 1)
	tag := model.ImageRef(res.WritableLayers[0].Tag)
	tagged, ok := tag.RestoredLayerSavepoint()
	require.True(t, ok, "%s is no restored layer tag", tag)
	assert.Equal(t, savepoint.ID, tagged)
	assert.True(t, strings.HasPrefix(string(tag), fmt.Sprintf("arcave/%s-app-1-busybox", env.project)), string(tag))

	rebuilt := env.replicas(t)
	require.Len(t, rebuilt, 1)
	assert.Equal(t, "original", env.read(t, rebuilt[0], layerMarker), "the writable layer")
	assert.Equal(t, "original", env.read(t, rebuilt[0], volumeMarker), "the volume beside it")

	// The restored layer names its upstream, so a savepoint taken on it still does.
	restored, err := env.daemon.ImageInspect(env.ctx, res.WritableLayers[0].Tag)
	require.NoError(t, err)
	require.NotNil(t, restored.Config)
	assert.Equal(t, savepoint.WritableLayers[0].UpstreamImageID.String(), restored.Config.Labels[string(model.SystemLabelBaseImageID)])
	assertSameImageRef(t, "busybox", restored.Config.Labels[string(model.SystemLabelBaseImageRef)])

	next, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "on-the-layer"})
	require.NoError(t, err)
	require.Len(t, next.WritableLayers, 1)
	assertSameImageRef(t, "busybox", next.WritableLayers[0].UpstreamImageRef)
	assert.Equal(t, savepoint.WritableLayers[0].UpstreamImageID, next.WritableLayers[0].UpstreamImageID)
	require.Len(t, next.Images, 1)
	assert.True(t, next.Images[0].Stored)
	assert.Zero(t, next.Images[0].StoredBytes, "the upstream is already in the store")
	t.Logf("upstream %s: first capture stored %d of %d bytes, second stored %d",
		next.Images[0].ID, savepoint.Images[0].StoredBytes,
		savepoint.Images[0].SizeBytes, next.Images[0].StoredBytes)
}

// A second rollback to the same savepoint loads nothing: the layer is in the daemon, and
// the same tag is put back on it, so the replica's image answers to one restored-layer
// tag however often it is rolled back to.
func TestRollback_AgainToTheSameSavepointPutsTheSameTagBack(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.up(t, 1)
	env.write(t, env.replicas(t)[0], "original")
	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "with-layer"})
	require.NoError(t, err)
	require.Len(t, savepoint.WritableLayers, 1)
	first, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})
	require.NoError(t, err)
	require.Len(t, first.WritableLayers, 1)
	env.write(t, env.replicas(t)[0], "drifted again")

	// Act
	second, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	require.Len(t, second.WritableLayers, 1)
	assert.Equal(t, first.WritableLayers[0].Tag, second.WritableLayers[0].Tag, "the tag names the savepoint alone")
	assert.Equal(t, first.WritableLayers[0].ImageID, second.WritableLayers[0].ImageID, "the same layer, loaded once")
	assert.Equal(t, "original", env.read(t, env.replicas(t)[0], layerMarker))

	restored, err := env.daemon.ImageInspect(env.ctx, second.WritableLayers[0].ImageID.String())
	require.NoError(t, err)
	var tags []string
	for _, ref := range restored.RepoTags {
		if model.ImageRef(ref).IsRestoredLayerTag() {
			tags = append(tags, string(model.ImageRef(ref).Familiar()))
		}
	}
	assert.Equal(t, []string{second.WritableLayers[0].Tag}, tags, "one restored-layer tag, put twice")
}

func TestRollback_RestoresEachReplicasOwnWritableLayer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.up(t, 2)
	before := env.replicas(t)
	require.Len(t, before, 2, "the compose project is scaled to two replicas")
	env.write(t, before[0], "replica-1")
	env.write(t, before[1], "replica-2")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "scaled"})
	require.NoError(t, err)
	require.Len(t, savepoint.WritableLayers, 2, "one capture per replica")

	env.write(t, env.replicas(t)[0], "drifted")
	env.write(t, env.replicas(t)[1], "drifted")

	// Act
	res, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	require.Len(t, res.WritableLayers, 2)

	// A compose service names one image, so the second replica is the one the
	// per-replica fixup had to rebuild.
	rebuilt := env.replicas(t)
	require.Len(t, rebuilt, 2)
	assert.Equal(t, "replica-1", env.read(t, rebuilt[0], layerMarker))
	assert.Equal(t, "replica-2", env.read(t, rebuilt[1], layerMarker))
}

// imageRollbackEnv is the compose project these tests drive, wired the way cmd/arcave wires it.
type imageRollbackEnv struct {
	ctx       context.Context
	svc       *core.Service
	compose   *composeclient.Client
	inspector *introspect.Inspector
	daemon    *client.Client
	store     storages.Storager
	project   string
	// deps built svc: a test that needs another capture policy over the same store builds from them.
	deps core.Deps
}

func newImageRollbackEnv(t *testing.T) *imageRollbackEnv {
	t.Helper()

	daemonAPI := newDaemonClient(t)
	project := "arcave-image-rollback-it-" + randomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	composeService, prompter := newComposeService(t, cli)
	profile := newRuntimeProfile(t, daemonAPI)
	inspector := introspect.NewInspector(profile.api, model.DockerComposeProjectName(project))
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second, Prompter: prompter,
	})

	projectRoot := t.TempDir()
	store, err := storefactory.New(t.Context(), config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, "arcave-image-rollback-tenant", model.DefaultSession, model.DockerComposeProjectName(project))
	require.NoError(t, err)

	// The blob store is a sibling of the savepoints inside the compose project root, so it
	// dies with the session the way everything else there does.
	blobStore, err := store.SubStorage(dedupcopy.NamespaceBlobs, true)
	require.NoError(t, err)

	repo := sprepo.New(store)
	images := newImageCopier(store, newLayerOwner("arcave-image-rollback-tenant", project), profile.engine, blobStore)
	registry := mustRegistry(t, profile.volumes)
	deps := core.Deps{
		Savepoints:     sessionstore.NewSavepoints(repo),
		ArcaveProject:  "arcave-image-rollback-tenant",
		SessionLocker:  newStoreLocker(store, model.DefaultSession),
		WriteAdmission: newPlainAdmission(store),
		ProjectLocker:  lock.New(projectRoot),
		Inflight:       inflightrepo.New(projectRoot),
		Workload: compose.New(compose.Deps{
			SavepointRepo:      repo,
			ImageBackend:       images,
			ComposeRunner:      runner,
			ContainerControl:   profile.engine,
			Store:              store,
			ProjectAssembler:   profile.assembler,
			SpecCodec:          composespec.NewCodec(),
			BackendRegistry:    registry,
			CaptureDefaults:    newCaptureDefaults(model.SnapshotBackendFullCopy),
			ComposeInspector:   inspector,
			VolumeRemover:      profile.engine,
			ContainerRebuilder: profile.engine,
			ImageLookup:        profile.engine,
		}),
	}
	svc := core.New(deps)

	env := &imageRollbackEnv{
		ctx:       zerolog.New(io.Discard).WithContext(context.Background()),
		svc:       svc,
		compose:   runner,
		inspector: inspector,
		daemon:    daemonAPI,
		store:     store,
		project:   project,
		deps:      deps,
	}
	t.Cleanup(func() { removeComposeProject(t, env.daemon, env.project) })
	return env
}

func (e *imageRollbackEnv) up(t *testing.T, replicas int) {
	t.Helper()

	e.upDocument(t, fmt.Sprintf(imageRollbackSpec, e.project, replicas))
}

// upDocument creates and starts the compose project a document describes.
func (e *imageRollbackEnv) upDocument(t *testing.T, document string) {
	t.Helper()

	project, err := composespec.NewCodec().Parse(e.ctx, []byte(document))
	require.NoError(t, err)
	require.NoError(t, e.compose.Create(e.ctx, project, nil))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
}

// replicas are the service's containers, sorted by replica number.
func (e *imageRollbackEnv) replicas(t *testing.T) []string {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err)
	require.Len(t, res.Project.Services, 1)

	ids := make([]string, 0, len(res.Project.Services[0].Containers))
	for _, c := range res.Project.Services[0].Containers {
		ids = append(ids, c.ID)
	}
	return ids
}

// write stamps content into both markers of one replica, past its mounts and into
// the volume, so one call drifts everything a rollback has to put back.
func (e *imageRollbackEnv) write(t *testing.T, containerID, content string) {
	t.Helper()

	e.exec(t, containerID, "sh", "-c",
		fmt.Sprintf("printf %%s %s > %s && printf %%s %s > %s", content, layerMarker, content, volumeMarker))
}

func (e *imageRollbackEnv) read(t *testing.T, containerID, path string) string {
	t.Helper()

	return e.exec(t, containerID, "cat", path)
}

// exec runs one command inside a running container and returns its output.
func (e *imageRollbackEnv) exec(t *testing.T, containerID string, cmd ...string) string {
	t.Helper()

	created, err := e.daemon.ExecCreate(e.ctx, containerID, client.ExecCreateOptions{
		Cmd: cmd, AttachStdout: true, AttachStderr: true,
	})
	require.NoError(t, err)

	attached, err := e.daemon.ExecAttach(e.ctx, created.ID, client.ExecAttachOptions{})
	require.NoError(t, err)
	defer attached.Close()

	var out bytes.Buffer
	_, err = stdcopy.StdCopy(&out, &out, attached.Reader)
	require.NoError(t, err)

	inspect, err := e.daemon.ExecInspect(e.ctx, created.ID, client.ExecInspectOptions{})
	require.NoError(t, err)
	require.Zero(t, inspect.ExitCode, out.String())
	return out.String()
}
