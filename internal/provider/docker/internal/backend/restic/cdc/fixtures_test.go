package cdc

import (
	"context"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// The volume every test captures: one named volume of the demo project, mounted
// by one service.
const (
	demoProject  = "demo"
	demoService  = "db"
	demoTarget   = "/var/lib/postgresql/data"
	demoObjectID = "volume-named-demo_pgdata"
	demoVolName  = "demo_pgdata"
	// demoMounted is where the volume lands inside the restic container, and so
	// the path inside every snapshot of it.
	demoMounted = "/m/" + demoObjectID
	// demoRepoURLBase is what the DI root resolved out of the restic server.
	demoRepoURLBase = "rest:http://host.docker.internal:5000"
	// demoRepoURL is what the backend addresses its repository at: the base, the
	// volume's ObjectID, and the protocol's trailing slash.
	demoRepoURL = demoRepoURLBase + "/" + demoObjectID + "/"
	// demoImage stands in for the configured restic image.
	demoImage = "restic/restic:0.19.1"
)

// The restic ndjson these tests feed back, taken from the shape a real run
// prints: progress lines, then one summary.
const (
	backupStatusLine  = `{"message_type":"status","seconds_elapsed":2,"percent_done":0.5,"total_files":3,"files_done":1}`
	backupSummaryLine = `{"message_type":"summary","files_new":3,"files_changed":0,"dirs_new":2,` +
		`"data_added":1048576,"total_files_processed":3,"total_bytes_processed":10485760,` +
		`"total_duration":1.5,"snapshot_id":"3f8a1c0d9b"}`
	restoreSummaryLine = `{"message_type":"summary","total_files":3,"files_restored":3,` +
		`"total_bytes":10485760,"bytes_restored":10485760}`
	// init prints one line of its own and no summary at all.
	initializedLine = `{"message_type":"initialized","id":"822a3dff7b73","repository":"/repo"}`
	// What a failing run puts on stderr: one error per item it could not handle,
	// then one exit_error carrying the diagnosis the CLI would have ended on.
	backupItemErrorLine = `{"message_type":"error","error":{"message":"read /m/volume-named-demo_pgdata/big.bin: ` +
		`input/output error"},"during":"archival","item":"/m/volume-named-demo_pgdata/big.bin"}`
	backupExitErrorLine = `{"message_type":"exit_error","code":3,` +
		`"message":"Warning: at least one source file could not be read"}`
)

// demoAddress is what the engine answers for demoVolume: a named volume is its
// docker name, whatever any service mounts it at.
var demoAddress = dockermodel.MountSource{Type: dockermodel.MountSourceVolume, Source: demoVolName}

// demoVolume is the unit under test.
func demoVolume() dockermodel.Volume {
	return dockermodel.Volume{
		ObjectID:       demoObjectID,
		ComposeProject: demoProject,
		Type:           dockermodel.VolumeTypeNamed,
		Backend:        dockermodel.SnapshotBackendCDC,
		Named: &dockermodel.VolumePropertiesNamed{
			VolumeName: demoVolName,
			UsedBy:     []dockermodel.ServiceReference{{Service: demoService, Target: demoTarget}},
		},
	}
}

// newStore is a real directory Storager on a tempdir: objectRepo addresses real
// objects, so a fake would only re-implement the backend.
func newStore(t *testing.T) storages.Storager {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// harness is one wired backend plus the doubles behind it.
type harness struct {
	snapshotter dockermodel.VolumeSnapshotBackend
	runner      *mockRunner
	root        storages.Storager
	// repo is the repository root re-rooted at this volume's repository — where
	// the test plants and reads the objects the server-side half addresses.
	repo storages.Storager
}

// newHarness wires the backend with opts, with the data plane's URL base already
// resolved and the mount already resolvable.
func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()

	root := newStore(t)
	repo, err := root.SubStorage(demoObjectID, true)
	require.NoError(t, err)

	runner := &mockRunner{}
	runner.On("ResolveMountSource", mock.Anything, demoVolume()).Return(demoAddress, nil).Maybe()

	snapshotter, err := NewFactory(Dependencies{
		Runner:         runner,
		RepoURLBase:    demoRepoURLBase,
		RepositoryRoot: root,
		Options:        opts,
	}).New(dockermodel.BackendSpec{Volume: demoVolume()})
	require.NoError(t, err)

	return &harness{
		snapshotter: snapshotter,
		runner:      runner,
		root:        root,
		repo:        repo,
	}
}

// defaultOptions is the wipe-mode configuration ApplyDefaults produces.
func defaultOptions() Options {
	return Options{Image: demoImage, Overwrite: "if-changed"}
}

// objectsUnder lists the names a prefix holds, relative to it.
func objectsUnder(t *testing.T, store storages.Storager, prefix string) []string {
	t.Helper()

	objects, err := store.List(context.Background(), prefix)
	require.NoError(t, err)
	names := make([]string, 0, len(objects))
	for _, obj := range objects {
		names = append(names, obj.Name)
	}
	return names
}
