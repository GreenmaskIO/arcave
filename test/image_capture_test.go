package test

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
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
    volumes:
      - data:/data
volumes:
  data: {}
`

func TestSavepoint_CommitsTheWritableLayer(t *testing.T) {
	// Arrange
	env := newImageCaptureEnv(t)
	env.up(t)

	// Act
	savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "with-layer"})

	// Assert
	require.NoError(t, err)
	require.Len(t, savepoint.Images, 1, "one container, one capture")
	capture := savepoint.Images[0]
	assert.Equal(t, "app", capture.Service)
	assert.Equal(t, 1, capture.Replica)
	assert.NotEmpty(t, capture.ImageID)
	assert.Equal(t, "busybox", capture.BaseImage)
	assert.Positive(t, capture.SizeBytes)

	// A save archive: the legacy manifest.json every supported daemon writes.
	key := filepath.Join(savepoint.ID.String(), imagecopy.ImagesNamespace, "app", "1", "image.tar")
	require.Contains(t, storeKeys(t, env.store), key)
	assert.Contains(t, tarEntries(t, env.store, key), "manifest.json")

	// The committed image is gone from the daemon: the store is the only copy.
	_, err = env.daemon.ImageInspect(env.ctx, capture.ImageID)
	assert.True(t, cerrdefs.IsNotFound(err), "the committed image outlived the capture: %v", err)
}

// imageCaptureEnv is the stack this test drives, wired the way cmd/arcave wires it.
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
	progress.Mode = progress.ModeQuiet

	engine := dockerclient.NewEngine(daemonAPI)
	runner := composeclient.NewClient(composeService, composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second,
	})

	projectRoot := t.TempDir()
	store, err := storefactory.New(config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}, projectRoot, "arcave-image-tenant", model.DefaultSession, model.StackName(project))
	require.NoError(t, err)

	svc := core.New(core.Deps{
		SavepointRepo:    sprepo.New(store),
		ProjectLocker:    lock.New(projectRoot),
		ComposeInspector: introspect.NewInspector(daemonAPI, model.StackName(project)),
		ProjectAssembler: composespec.NewAssembler(),
		ComposeRunner:    runner,
		VolumeRemover:    engine,
		ImageCapturer:    imagecopy.New(store, engine),
		BackendRegistry:  mustRegistry(t, engine),
		Inflight:         inflightrepo.New(projectRoot),
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})

	env := &imageCaptureEnv{
		ctx:     zerolog.New(io.Discard).WithContext(context.Background()),
		svc:     svc,
		compose: runner,
		daemon:  daemonAPI,
		store:   store,
		project: project,
	}
	t.Cleanup(func() {
		assert.NoError(t, env.compose.Down(env.ctx, &types.Project{Name: env.project}))
	})
	return env
}

func (e *imageCaptureEnv) up(t *testing.T) {
	t.Helper()

	document := fmt.Sprintf(imageCaptureSpec, e.project, layerMarker)
	project, err := composespec.NewAssembler().Parse(e.ctx, []byte(document))
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

// tarEntries names the top-level entries of a stored archive.
func tarEntries(t *testing.T, store storages.Storager, key string) []string {
	t.Helper()

	src, err := store.GetObject(context.Background(), key)
	require.NoError(t, err)
	defer func() { assert.NoError(t, src.Close()) }()

	var names []string
	reader := tar.NewReader(src)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, header.Name)
	}
	return names
}
