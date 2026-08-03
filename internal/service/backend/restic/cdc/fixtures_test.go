package cdc

import (
	"context"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	resticmodel "github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// The volume every test captures: one named volume of the demo project, mounted
// by one service.
const (
	demoProject  = "demo"
	demoService  = "db"
	demoTarget   = "/var/lib/postgresql/data"
	demoObjectID = "volume/demo_pgdata"
	demoVolName  = "demo_pgdata"
	// demoMounted is where the volume lands inside the restic container, and so
	// the path inside every snapshot of it.
	demoMounted = "/m/" + demoObjectID
	// demoRepoURLBase is what the DI root resolved out of the restic server.
	demoRepoURLBase = "rest:http://host.docker.internal:5000"
	// demoRepoURL is what the backend addresses its repository at: the base, the
	// publish prefix, and the protocol's trailing slash.
	demoRepoURL = demoRepoURLBase + "/savepoint-volume-demo_pgdata/"
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
)

// demoAddress is what the engine answers for demoVolume: a named volume is its
// docker name, whatever any service mounts it at.
var demoAddress = model.VolumeAddress{Kind: model.VolumeAddressVolume, Source: demoVolName}

// demoVolume is the unit under test.
func demoVolume() model.Volume {
	return model.Volume{
		ObjectID:       demoObjectID,
		ComposeProject: demoProject,
		Type:           model.VolumeTypeNamed,
		Backend:        model.SnapshotBackendCDC,
		Named: &model.VolumePropertiesNamed{
			VolumeName: demoVolName,
			UsedBy:     []model.ServiceReference{{Service: demoService, Target: demoTarget}},
		},
	}
}

// newStore is a real directory Storager on a tempdir: the repository admin
// addresses real objects, so a fake would only re-implement the backend.
func newStore(t *testing.T) storages.Storager {
	t.Helper()

	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// harness is one wired backend plus the doubles behind it.
type harness struct {
	snapshotter model.VolumeSnapshotBackend
	runner      *mockRunner
	publisher   *mockPublisher
	shared      storages.Storager
	// repo is the shared store re-rooted at this volume's repository — where the
	// test plants and reads the objects the server-side half addresses.
	repo storages.Storager
}

// newHarness wires the backend for tier with opts, with the data plane's URL
// base already resolved and the mount already resolvable.
func newHarness(t *testing.T, tier model.BackendTier, opts Options) *harness {
	t.Helper()

	shared := newStore(t)
	repo, err := shared.SubStorage(repoNamespace+"/"+demoObjectID, true)
	require.NoError(t, err)

	runner := &mockRunner{}
	runner.On("ResolveVolumeAddress", mock.Anything, demoVolume()).Return(demoAddress, nil).Maybe()
	publisher := &mockPublisher{}
	// Both are Maybe: half the contract's calls publish nothing at all, and a case
	// that is about the publishing counts the calls itself.
	publisher.On("Register", repositoryPrefix(tier, demoObjectID), matchRepository(t, repo)).Return(nil).Maybe()
	publisher.On("Unregister", repositoryPrefix(tier, demoObjectID)).Return().Maybe()

	snapshotter, err := NewFactory(Dependencies{
		Runner:      runner,
		Publisher:   publisher,
		RepoURLBase: demoRepoURLBase,
		SharedStore: shared,
		Options:     opts,
	}).New(model.BackendSpec{Volume: demoVolume(), Tier: tier})
	require.NoError(t, err)

	return &harness{
		snapshotter: snapshotter,
		runner:      runner,
		publisher:   publisher,
		shared:      shared,
		repo:        repo,
	}
}

// defaultOptions is the wipe-mode configuration ApplyDefaults produces.
func defaultOptions() Options {
	return Options{Image: demoImage, Overwrite: "if-changed"}
}

// markerKey is a key of the shape restic writes, planted under keys/ — the one
// namespace no cdc operation reads or clears.
const markerKey = "9999999999999999999999999999999999999999999999999999999999999999"

// matchRepository matches the repository a Register was handed by what it can
// see: the factory builds it over a store of its own, so its root is nameable
// only through its own contract, and only a repository rooted at want reads the
// marker planted under it.
func matchRepository(t *testing.T, want storages.Storager) any {
	t.Helper()

	ctx := context.Background()
	require.NoError(t, want.PutObject(ctx, "keys/"+markerKey, strings.NewReader("marker")))

	return mock.MatchedBy(func(got resticmodel.Repository) bool {
		blobs, err := got.ListBlobs(ctx, resticmodel.ListBlobsRequest{Type: resticmodel.TypeKeys})
		return err == nil && len(blobs) == 1 && blobs[0].Name == markerKey
	})
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
