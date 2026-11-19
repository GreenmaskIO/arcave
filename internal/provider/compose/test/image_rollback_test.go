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

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/greenmaskio/storages"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose"
	composeclient "github.com/greenmaskio/arcave/internal/provider/compose/internal/client/compose"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

func TestRollback_RestoresTheWritableLayer(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newImageRollbackEnv(t)
	env.up(t, 1)
	env.write(t, env.replicas(t)[0], "original")

	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "with-layer"})
	require.NoError(t, err)
	require.Len(t, detailOf(t, savepoint).WritableLayers, 1)

	// The layer drifts, and so does the volume beside it.
	env.write(t, env.replicas(t)[0], "drifted")

	// Act
	res, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	require.Len(t, restoreDetailOf(t, res.Detail).WritableLayers, 1)
	tag := model.ImageRef(restoreDetailOf(t, res.Detail).WritableLayers[0].Tag)
	tagged, ok := composemodel.ParseRestoredLayerTag(tag)
	require.True(t, ok, "%s is no restored layer tag", tag)
	assert.Equal(t, savepoint.ID, tagged)
	assert.True(t, strings.HasPrefix(string(tag), fmt.Sprintf("arcave/%s-app-1-busybox", env.project)), string(tag))

	rebuilt := env.replicas(t)
	require.Len(t, rebuilt, 1)
	assert.Equal(t, "original", env.read(t, rebuilt[0], daemontest.LayerMarker), "the writable layer")
	assert.Equal(t, "original", env.read(t, rebuilt[0], daemontest.VolumeMarker), "the volume beside it")

	// The restored layer names its upstream, so a savepoint taken on it still does.
	restored, err := env.daemon.ImageInspect(env.ctx, restoreDetailOf(t, res.Detail).WritableLayers[0].Tag)
	require.NoError(t, err)
	require.NotNil(t, restored.Config)
	assert.Equal(t, detailOf(t, savepoint).WritableLayers[0].UpstreamImageID.String(), restored.Config.Labels[string(composemodel.SystemLabelBaseImageID)])
	assertSameImageRef(t, "busybox", restored.Config.Labels[string(composemodel.SystemLabelBaseImageRef)])

	next, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "on-the-layer"})
	require.NoError(t, err)
	require.Len(t, detailOf(t, next).WritableLayers, 1)
	assertSameImageRef(t, "busybox", detailOf(t, next).WritableLayers[0].UpstreamImageRef)
	assert.Equal(t, detailOf(t, savepoint).WritableLayers[0].UpstreamImageID, detailOf(t, next).WritableLayers[0].UpstreamImageID)
	require.Len(t, detailOf(t, next).Images, 1)
	assert.True(t, detailOf(t, next).Images[0].Stored)
	assert.Zero(t, detailOf(t, next).Images[0].StoredBytes, "the upstream is already in the store")
	t.Logf("upstream %s: first capture stored %d of %d bytes, second stored %d",
		detailOf(t, next).Images[0].ID, detailOf(t, savepoint).Images[0].StoredBytes,
		detailOf(t, savepoint).Images[0].SizeBytes, detailOf(t, next).Images[0].StoredBytes)
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
	require.Len(t, detailOf(t, savepoint).WritableLayers, 1)
	first, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})
	require.NoError(t, err)
	require.Len(t, restoreDetailOf(t, first.Detail).WritableLayers, 1)
	env.write(t, env.replicas(t)[0], "drifted again")

	// Act
	second, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	require.Len(t, restoreDetailOf(t, second.Detail).WritableLayers, 1)
	assert.Equal(t, restoreDetailOf(t, first.Detail).WritableLayers[0].Tag, restoreDetailOf(t, second.Detail).WritableLayers[0].Tag, "the tag names the savepoint alone")
	assert.Equal(t, restoreDetailOf(t, first.Detail).WritableLayers[0].ImageID, restoreDetailOf(t, second.Detail).WritableLayers[0].ImageID, "the same layer, loaded once")
	assert.Equal(t, "original", env.read(t, env.replicas(t)[0], daemontest.LayerMarker))

	restored, err := env.daemon.ImageInspect(env.ctx, restoreDetailOf(t, second.Detail).WritableLayers[0].ImageID.String())
	require.NoError(t, err)
	var tags []string
	for _, ref := range restored.RepoTags {
		if composemodel.IsRestoredLayerTag(model.ImageRef(ref)) {
			tags = append(tags, string(model.ImageRef(ref).Familiar()))
		}
	}
	assert.Equal(t, []string{restoreDetailOf(t, second.Detail).WritableLayers[0].Tag}, tags, "one restored-layer tag, put twice")
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
	require.Len(t, detailOf(t, savepoint).WritableLayers, 2, "one capture per replica")

	env.write(t, env.replicas(t)[0], "drifted")
	env.write(t, env.replicas(t)[1], "drifted")

	// Act
	res, err := env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

	// Assert
	require.NoError(t, err)
	require.Len(t, restoreDetailOf(t, res.Detail).WritableLayers, 2)

	// A compose service names one image, so the second replica is the one the
	// per-replica fixup had to rebuild.
	rebuilt := env.replicas(t)
	require.Len(t, rebuilt, 2)
	assert.Equal(t, "replica-1", env.read(t, rebuilt[0], daemontest.LayerMarker))
	assert.Equal(t, "replica-2", env.read(t, rebuilt[1], daemontest.LayerMarker))
}

// imageRollbackEnv is the compose project these tests drive, its workload opened
// through compose.Open, as the CLI opens it.
type imageRollbackEnv struct {
	ctx       context.Context
	svc       *core.Service
	compose   *composeclient.Client
	inspector *introspect.Reader
	daemon    *client.Client
	store     storages.Storager
	project   string
	// deps built svc: a test that needs another capture policy over the same store builds from them.
	deps core.Deps
}

func newImageRollbackEnv(t *testing.T) *imageRollbackEnv {
	t.Helper()

	daemonAPI := daemontest.NewClient(t)
	project := "arcave-image-rollback-it-" + daemontest.RandomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	composeService, prompter := newComposeService(t, cli)
	profile := newRuntimeProfile(t, daemonAPI)
	inspector := introspect.NewReader(profile.API, model.DockerComposeProjectName(project))
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second, Prompter: prompter,
	})

	// Unique per test: the inflight marker is the arcave project's on the shared daemon.
	arcaveProject := "arcave-image-rollback-tenant-" + daemontest.RandomSuffix(t)
	projectRoot := t.TempDir()
	store, err := storefactory.New(t.Context(), config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, model.ArcaveProjectName(arcaveProject), model.DefaultSession, model.DockerComposeProjectName(project))
	require.NoError(t, err)

	deps := core.Deps{
		Savepoints:     compose.NewStoreSavepoints(store),
		Store:          store,
		ArcaveProject:  model.ArcaveProjectName(arcaveProject),
		ComposeProject: model.DockerComposeProjectName(project),
		SessionLocker:  newStoreLocker(store, model.DefaultSession),
		WriteAdmission: newPlainAdmission(store),
		ProjectLocker:  lock.New(projectRoot),
		Inflight:       openInflightMarkers(t, arcaveProject, project),
		Workload:       openComposeWorkload(t, store, arcaveProject, project),
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
	t.Cleanup(func() { daemontest.RemoveComposeProject(t, env.daemon, env.project) })
	return env
}

func (e *imageRollbackEnv) up(t *testing.T, replicas int) {
	t.Helper()

	e.upDocument(t, fmt.Sprintf(daemontest.ImageRollbackSpec, e.project, replicas))
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

	daemontest.WriteMarkers(t, e.daemon, containerID, content)
}

func (e *imageRollbackEnv) read(t *testing.T, containerID, path string) string {
	t.Helper()

	return e.exec(t, containerID, "cat", path)
}

// exec runs one command inside a running container and returns its output.
func (e *imageRollbackEnv) exec(t *testing.T, containerID string, cmd ...string) string {
	t.Helper()

	return daemontest.Exec(t, e.daemon, containerID, cmd...)
}
