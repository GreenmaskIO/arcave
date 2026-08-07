package resticrest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
)

// The transport's proof: a stock restic container reaches the served endpoint
// through the whole chain — the container's own name for the host, the echo app,
// the repository registry — and drives a repository through init, backup, an
// incremental backup and a byte-identical restore. Throughput goes to the log as
// a reference, never as an assertion.

// resticImage pins the stock image the one-shot runner drives.
const resticImage = "restic/restic:0.19.0"

// busyboxImage fills, hashes and wipes the volume.
const busyboxImage = "busybox"

const e2ePassword = "arcave-e2e"

// e2eContainerHost is how the container names the host this server listens on.
// It is the config default, spelled here because the transport keeps no default
// of its own: the name a container resolves is the caller's to supply.
const e2eContainerHost = "host.docker.internal"

// blobMiB is the volume payload: random bytes, since zeros would deduplicate
// into a repository that proves nothing. A bind mount is not an option —
// virtiofs breaks direct-io reads on Docker Desktop.
const blobMiB = 1024

// mutateMiB is the second backup's change, ~1% of the blob.
const mutateMiB = 12

// containerRunner is the seam the cdc backend declares; the e2e drives every
// container through it to prove the one method is enough.
type containerRunner interface {
	RunOneShot(ctx context.Context, spec model.OneShotSpec) error
}

func TestResticEndToEnd(t *testing.T) {
	// Arrange: repository root → resolver → served endpoint on the host loopback.
	cli := newDockerClient(t)
	ctx := t.Context()

	root, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	// The name on the wire is a volume's arcave ObjectID: one segment, and the
	// name of the directory its repository lives in under the root.
	const objectID = "volume-service-db-var-lib-data"
	store, err := root.SubStorage(objectID, true)
	require.NoError(t, err)

	srv := rest.NewEcho(ctx, repo.NewResolver(root))
	port, err := rest.Serve(srv, "")
	require.NoError(t, err)
	defer func() { assert.NoError(t, srv.Close()) }()
	urlBase := rest.RepositoryURLBase("", e2eContainerHost, port)
	repoURL := urlBase + "/" + objectID + "/"

	var runner containerRunner = docker.NewEngine(cli)
	vol := newDataVolume(t, cli, runner)
	before := hashVolume(t, runner, vol)

	// Act & Assert: one lifecycle, each step gated on the previous one.

	// Init lands the repository in the Storager, through the whole chain.
	runRestic(t, runner, repoURL, "", "init")
	exists, err := store.Exists(ctx, "config")
	require.NoError(t, err)
	require.True(t, exists, "init must land the repository config in the store")

	// A repository that was never published is refused: the run fails with the
	// container's own exit.
	err = resticErr(t, runner, urlBase+"/no-such-prefix/", "", "cat", "config")
	require.Error(t, err, "an unregistered prefix must not reach a repository")
	var exitErr *model.ContainerExitError
	require.ErrorAs(t, err, &exitErr)
	assert.NotZero(t, exitErr.StatusCode)

	// First backup: the full payload crosses the boundary.
	first := backupSummaryOf(t, runRestic(t, runner, repoURL, vol, "backup", "--json", "/data"))
	require.Positive(t, first.TotalBytesProcessed)
	t.Logf("backup #1: %d bytes in %.1fs → %.1f MiB/s",
		first.TotalBytesProcessed, first.TotalDuration, mibPerSecond(first.TotalBytesProcessed, first.TotalDuration))

	// ~1% mutates; the second backup must ship an increment, not the payload.
	mutateVolume(t, runner, vol)
	mutated := hashVolume(t, runner, vol)
	require.NotEqual(t, before, mutated, "the mutation must actually change the volume")
	second := backupSummaryOf(t, runRestic(t, runner, repoURL, vol, "backup", "--json", "/data"))
	t.Logf("backup #2: %d bytes added in %.1fs", second.DataAdded, second.TotalDuration)
	assert.Less(t, second.DataAdded, first.TotalBytesProcessed/8,
		"an incremental backup after a ~1%% mutation must not reship the payload")

	// Wipe, then restore: the volume comes back byte for byte.
	wipeVolume(t, runner, vol)
	restoreStart := time.Now()
	runRestic(t, runner, repoURL, vol, "restore", "latest", "--target", "/")
	t.Logf("restore: %s", time.Since(restoreStart).Round(time.Second))
	after := hashVolume(t, runner, vol)
	require.Equal(t, mutated, after, "the restored volume must hash identically to the state the latest backup covered")
}

// newDockerClient skips unless a docker arcaved is reachable, and returns its
// client, closed with the test.
func newDockerClient(t *testing.T) *client.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the restic transport e2e test in short mode")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	if _, err := cli.Ping(context.Background()); err != nil {
		_ = cli.Close()
		t.Skip("skipping the restic transport e2e test: no reachable docker arcaved: " + err.Error())
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// newDataVolume creates a named volume filled with random data — one large blob
// plus a nested and a small file, so more than one shape crosses the boundary —
// and removes it with the test.
func newDataVolume(t *testing.T, cli *client.Client, runner containerRunner) string {
	t.Helper()

	suffix := make([]byte, 6)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	name := "arcave-restic-e2e-" + hex.EncodeToString(suffix)

	_, err = cli.VolumeCreate(context.Background(), volume.CreateOptions{Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), name, true) })

	busybox(t, runner, name, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1M count=%d && mkdir -p /data/nested && dd if=/dev/urandom of=/data/nested/inner bs=1M count=8 && dd if=/dev/urandom of=/data/small bs=1k count=4",
		blobMiB))
	return name
}

// mutateVolume overwrites mutateMiB in the middle of the blob, leaving its size
// alone.
func mutateVolume(t *testing.T, runner containerRunner, vol string) {
	t.Helper()
	busybox(t, runner, vol, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1M count=%d seek=%d conv=notrunc", mutateMiB, blobMiB/3))
}

// wipeVolume clears the volume the way the restore path's wipe policy does.
func wipeVolume(t *testing.T, runner containerRunner, vol string) {
	t.Helper()
	busybox(t, runner, vol, "find /data -mindepth 1 -maxdepth 1 -exec rm -rf {} +")
}

// hashVolume returns a sorted sha256 listing of every file in the volume.
func hashVolume(t *testing.T, runner containerRunner, vol string) string {
	t.Helper()
	sums := busybox(t, runner, vol, "find /data -type f -exec sha256sum {} + | sort")
	require.NotEmpty(t, sums, "the volume must hold files to hash")
	return sums
}

// busybox runs one shell line in a busybox container with the volume at /data
// and returns its stdout.
func busybox(t *testing.T, runner containerRunner, vol, script string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := runner.RunOneShot(t.Context(), model.OneShotSpec{
		Image:  busyboxImage,
		Cmd:    []string{"sh", "-c", script},
		Binds:  []string{vol + ":/data"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	require.NoErrorf(t, err, "busybox %q failed:\nstdout:\n%s\nstderr:\n%s", script, stdout.String(), stderr.String())
	return stdout.String()
}

// runRestic runs one restic command in a stock container against repoURL and
// returns its stdout; vol, when set, is mounted at /data.
func runRestic(t *testing.T, runner containerRunner, repoURL, vol string, args ...string) string {
	t.Helper()

	stdout, err := resticOut(t, runner, repoURL, vol, args...)
	require.NoError(t, err)
	return stdout
}

// resticErr is runRestic for the runs that are supposed to fail: the error comes
// back instead of failing the test.
func resticErr(t *testing.T, runner containerRunner, repoURL, vol string, args ...string) error {
	t.Helper()

	_, err := resticOut(t, runner, repoURL, vol, args...)
	return err
}

func resticOut(t *testing.T, runner containerRunner, repoURL, vol string, args ...string) (string, error) {
	t.Helper()

	spec := model.OneShotSpec{
		Image: resticImage,
		Cmd:   args,
		Env: []string{
			"RESTIC_REPOSITORY=" + repoURL,
			"RESTIC_PASSWORD=" + e2ePassword,
		},
	}
	if vol != "" {
		spec.Binds = []string{vol + ":/data"}
	}
	var stdout, stderr bytes.Buffer
	spec.Stdout, spec.Stderr = &stdout, &stderr

	if err := runner.RunOneShot(t.Context(), spec); err != nil {
		return stdout.String(), fmt.Errorf("restic %s:\nstdout:\n%s\nstderr:\n%s\n%w",
			strings.Join(args, " "), stdout.String(), stderr.String(), err)
	}
	return stdout.String(), nil
}

// backupSummary is the tail of restic backup --json: one summary line among the
// progress lines.
type backupSummary struct {
	MessageType         string  `json:"message_type"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	DataAdded           int64   `json:"data_added"`
	TotalDuration       float64 `json:"total_duration"`
}

// backupSummaryOf digs the summary line out of a backup's ndjson stdout.
func backupSummaryOf(t *testing.T, stdout string) backupSummary {
	t.Helper()

	for _, line := range strings.Split(stdout, "\n") {
		var s backupSummary
		if json.Unmarshal([]byte(line), &s) == nil && s.MessageType == "summary" {
			return s
		}
	}
	t.Fatalf("no summary line in restic backup output:\n%s", stdout)
	return backupSummary{}
}

func mibPerSecond(bytes int64, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(bytes) / seconds / (1 << 20)
}
