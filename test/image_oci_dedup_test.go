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

// Package test drives integration tests that need a live docker daemon; they
// self-skip without one.
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

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
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
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// epochMarker is what these tests drift and read back. Not layerMarker: the
// service's own command rewrites that one on every start.
const epochMarker = "/epoch-marker"

// sharedBlobPrefix is where the shared blob store sits under one stack root.
const sharedBlobPrefix = "shared/oci/blobs"

// ociDedupEnv is the stack for OCI blob-store deduplication tests.
type ociDedupEnv struct {
	ctx         context.Context
	svc         *core.Service
	compose     *composeclient.Client
	daemon      *client.Client
	stackStore  storages.Storager
	blobStore   storages.Storager
	projectRoot string
	project     string
}

func newOCIDedupEnv(t *testing.T) *ociDedupEnv {
	t.Helper()

	daemonAPI := newDaemonClient(t)
	project := "arcave-oci-dedup-" + randomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	composeService := dockercompose.NewComposeService(cli)
	progress.Mode = progress.ModeQuiet

	engine := dockerclient.NewEngine(daemonAPI)
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second,
	})

	projectRoot := t.TempDir()
	stackStore, err := storefactory.New(config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, "arcave-oci-tenant", model.DefaultSession, model.DockerComposeProjectName(project))
	require.NoError(t, err)

	blobStore, err := stackStore.SubStorage(sharedBlobPrefix, true)
	require.NoError(t, err)

	svc := core.New(core.Deps{
		SavepointRepo:      sprepo.New(stackStore),
		ProjectLocker:      lock.New(projectRoot),
		ComposeInspector:   introspect.NewInspector(daemonAPI, model.DockerComposeProjectName(project)),
		ProjectAssembler:   composespec.NewAssembler(),
		ComposeRunner:      runner,
		VolumeRemover:      engine,
		ImageBackend:       imagecopy.NewCopier(stackStore, engine, dedupcopy.New(blobStore)),
		ContainerRebuilder: engine,
		BackendRegistry:    mustRegistry(t, engine),
		Inflight:           inflightrepo.New(projectRoot),
		Store:              stackStore,
		DefaultBackend:     model.SnapshotBackendCopy,
	})

	env := &ociDedupEnv{
		ctx:         zerolog.New(io.Discard).WithContext(context.Background()),
		svc:         svc,
		compose:     runner,
		daemon:      daemonAPI,
		stackStore:  stackStore,
		blobStore:   blobStore,
		projectRoot: projectRoot,
		project:     project,
	}
	t.Cleanup(func() {
		assert.NoError(t, env.compose.Down(env.ctx, &types.Project{Name: env.project}))
		removeProjectNetworks(t, env.daemon, env.project)
	})
	return env
}

func (e *ociDedupEnv) up(t *testing.T) {
	t.Helper()

	document := fmt.Sprintf(imageCaptureSpec, e.project, layerMarker)
	project, err := composespec.NewAssembler().Parse(e.ctx, []byte(document))
	require.NoError(t, err)
	require.NoError(t, e.compose.Create(e.ctx, project))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
}

// blobStoreKeys lists the blob store's whole tree (blobs + markers).
func blobStoreKeys(t *testing.T, store storages.Storager) []string {
	t.Helper()

	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Name)
	}
	return keys
}

// TestSavepoint_TwoConsecutiveSavepoints_DeduplicatesBlobs pins that a second
// savepoint reuses the first one's blobs instead of writing them again.
func TestSavepoint_TwoConsecutiveSavepoints_DeduplicatesBlobs(t *testing.T) {
	// Arrange
	env := newOCIDedupEnv(t)
	env.up(t)

	// Act: first savepoint
	first, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "first"})
	require.NoError(t, err)
	require.Len(t, first.Images, 1)

	firstBlobKeys := blobStoreKeys(t, env.blobStore)
	firstBlobCount := len(blobOnlyKeys(firstBlobKeys))
	t.Logf("first savepoint: %d blobs, %d total objects", firstBlobCount, len(firstBlobKeys))

	env.mutateContainer(t, "second-epoch")

	// Act: second savepoint
	second, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "second"})
	require.NoError(t, err)
	require.Len(t, second.Images, 1)

	secondBlobCount := len(blobOnlyKeys(blobStoreKeys(t, env.blobStore)))
	t.Logf("second savepoint: %d blobs, %d of them new", secondBlobCount, secondBlobCount-firstBlobCount)

	// Assert: bytes, not blob count — how many metadata blobs a save archive carries
	// is the daemon's business; the classic image store adds a legacy json per layer.
	capture := second.Images[0]
	assert.Less(t, capture.StoredBytes, capture.SizeBytes/10,
		"the second savepoint stored %d of the archive's %d bytes: the base layer went in again",
		capture.StoredBytes, capture.SizeBytes)

	assert.NotEmpty(t, first.Images[0].ImageID, "first savepoint image ID")
	assert.NotEmpty(t, second.Images[0].ImageID, "second savepoint image ID")

	_, err = env.daemon.ImageInspect(env.ctx, first.Images[0].ImageID.String())
	assert.True(t, dockerIsNotFound(err), "first image should be gone from daemon: %v", err)
	_, err = env.daemon.ImageInspect(env.ctx, second.Images[0].ImageID.String())
	assert.True(t, dockerIsNotFound(err), "second image should be gone from daemon: %v", err)
}

// TestSavepoint_TwoSavepoints_BothRestore rolls back to a savepoint whose blobs a
// later one shares, so the layer has to come back out of the blob store.
func TestSavepoint_TwoSavepoints_BothRestore(t *testing.T) {
	// Arrange
	env := newOCIDedupEnv(t)
	env.up(t)
	env.mutateContainer(t, "first-epoch")

	first, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "first"})
	require.NoError(t, err)
	require.Len(t, first.Images, 1)

	env.mutateContainer(t, "second-epoch")
	second, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "second"})
	require.NoError(t, err)
	require.Len(t, second.Images, 1)
	require.NotEqual(t, first.Images[0].ImageID, second.Images[0].ImageID,
		"the two savepoints must differ, or deduplication is trivially true")
	assert.Less(t, second.Images[0].StoredBytes, second.Images[0].SizeBytes,
		"the second savepoint reuses the first one's base layers")

	// Act
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &first.ID})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "first-epoch", env.exec(t, env.containerID(t), "cat", epochMarker),
		"the writable layer came back out of the blob store")
}

// blobOnlyKeys keeps the blobs, which live one algorithm segment down.
func blobOnlyKeys(keys []string) []string {
	var result []string
	for _, k := range keys {
		if strings.HasPrefix(k, "sha256/") {
			result = append(result, k)
		}
	}
	return result
}

func dockerIsNotFound(err error) bool {
	return errdefs.IsNotFound(err)
}

// mutateContainer stamps content into the running container's writable layer, so
// the next savepoint has something of its own to commit.
func (e *ociDedupEnv) mutateContainer(t *testing.T, content string) {
	t.Helper()

	e.exec(t, e.containerID(t), "sh", "-c", fmt.Sprintf("printf %%s %s > %s", content, epochMarker))
}

// containerID is the stack's one container.
func (e *ociDedupEnv) containerID(t *testing.T) string {
	t.Helper()

	res, err := introspect.NewInspector(e.daemon, model.DockerComposeProjectName(e.project)).Introspect(e.ctx)
	require.NoError(t, err)
	require.Len(t, res.Project.Services, 1)
	require.Len(t, res.Project.Services[0].Containers, 1)
	return res.Project.Services[0].Containers[0].ID
}

// exec runs one command inside the running container and returns its output.
func (e *ociDedupEnv) exec(t *testing.T, containerID string, cmd ...string) string {
	t.Helper()

	created, err := e.daemon.ContainerExecCreate(e.ctx, containerID, container.ExecOptions{
		Cmd: cmd, AttachStdout: true, AttachStderr: true,
	})
	require.NoError(t, err)

	attached, err := e.daemon.ContainerExecAttach(e.ctx, created.ID, container.ExecAttachOptions{})
	require.NoError(t, err)
	defer attached.Close()

	var out bytes.Buffer
	_, err = stdcopy.StdCopy(&out, &out, attached.Reader)
	require.NoError(t, err)

	inspect, err := e.daemon.ContainerExecInspect(e.ctx, created.ID)
	require.NoError(t, err)
	require.Zero(t, inspect.ExitCode, out.String())
	return out.String()
}
