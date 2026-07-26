package rest

import (
	"context"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is the protocol's proof: a real restic binary, over real TCP, driven
// through the whole lifecycle it puts a repository through — init, backup,
// check, restore, forget, prune, check again — against this handler and a
// directory store. Everything downstream (the stdio tunnel, the CDC backend)
// only moves these bytes around, so if this passes the protocol is not where the
// architecture will break.
//
// It is guarded like the container tests: -short skips it, and so does a host
// with no restic on PATH.

// resticPassword is the repository password. The repository lives in a temp dir
// for the length of one test, so it is a constant, not a secret.
const resticPassword = "arcave-conformance"

func TestHandler_ConformsToRestic(t *testing.T) {
	// Arrange
	resticBin := requireRestic(t)

	store, root := newConformanceStore(t)
	srv := httptest.NewServer(New(store))
	defer srv.Close()

	fixture := newFixture(t)
	restic := resticRunner(t, resticBin, srv.URL)

	// Act & Assert: one lifecycle, each step gated on the previous one.
	restic(t, "init")
	assertStored(t, store, "config")

	restic(t, "backup", fixture)
	first := restic(t, "snapshots", "--json")
	require.Contains(t, first, `"id"`, "the first backup produced no snapshot")

	// A second backup over changed data, so forget has something to drop and
	// prune has something to repack.
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "changing.txt"), []byte(strings.Repeat("second", 4096)), 0o600))
	restic(t, "backup", fixture)

	restic(t, "check", "--read-data")
	for _, prefix := range []string{"data", "index", "snapshots"} {
		stats, err := store.List(context.Background(), prefix)
		require.NoError(t, err)
		assert.NotEmpty(t, stats, "the store holds no %s objects after a backup", prefix)
	}

	target := t.TempDir()
	restic(t, "restore", "latest", "--target", target)
	assertRestored(t, fixture, target)

	restic(t, "forget", "--keep-last", "1", "--prune")
	restic(t, "check", "--read-data")

	// Nothing but the repository's own objects may have appeared under the store
	// root: the handler writes flat keys and nothing else.
	assertFlatLayout(t, root)
}

// requireRestic skips unless a real restic binary can be exercised, and returns
// its path.
func requireRestic(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping restic conformance test in short mode")
	}
	path, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("skipping restic conformance test: no restic binary on PATH")
	}
	return path
}

// newConformanceStore returns the store the handler serves, plus its root
// directory so the on-disk layout can be asserted afterwards.
func newConformanceStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{Path: root})
	require.NoError(t, err)
	return st, root
}

// newFixture writes the directory restic backs up: a few small files and one
// large enough to span more than a single blob, so packs and ranged reads are
// both exercised.
func newFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"small.txt":     "hello arcave",
		"changing.txt":  strings.Repeat("first", 4096),
		"nested/db.sql": strings.Repeat("INSERT INTO t VALUES (1);\n", 8192),
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	return dir
}

// resticRunner returns a function that runs one restic command against the
// served repository and fails the test with the command's own output.
//
// The default connection count is left alone on purpose: production runs
// rest.connections=1, but the handler must be concurrency-safe, and letting
// restic use its five is the only test that says so.
func resticRunner(t *testing.T, bin, serverURL string) func(*testing.T, ...string) string {
	t.Helper()
	return func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = append(os.Environ(),
			"RESTIC_REPOSITORY=rest:"+serverURL+"/",
			"RESTIC_PASSWORD="+resticPassword,
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "restic %s failed:\n%s", strings.Join(args, " "), out)
		return string(out)
	}
}

// assertStored fails unless the store holds key — the handler's writes must land
// in the Storager, not in some directory restic reached on its own.
func assertStored(t *testing.T, store storages.Storager, key string) {
	t.Helper()
	exists, err := store.Exists(context.Background(), key)
	require.NoError(t, err)
	assert.True(t, exists, "expected %s in the store", key)
}

// assertRestored compares what came back out of the repository with what went
// in. restic recreates the source's absolute path under the target, so files are
// matched by their path relative to the fixture rather than by a computed
// prefix.
func assertRestored(t *testing.T, fixture, target string) {
	t.Helper()

	restored := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path) //nolint:gosec // a path this walk produced
		if err != nil {
			return err
		}
		restored[d.Name()] = content
		return nil
	}))

	require.NoError(t, filepath.WalkDir(fixture, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := os.ReadFile(path) //nolint:gosec // a path this walk produced
		if err != nil {
			return err
		}
		got, ok := restored[d.Name()]
		if !assert.Truef(t, ok, "%s was not restored", d.Name()) {
			return nil
		}
		assert.Equalf(t, want, got, "%s came back different", d.Name())
		return nil
	}))
}

// assertFlatLayout pins the layout deviation that matters most: rest-server
// shards data/ over 256 sub-directories, this handler does not, and every object
// therefore sits exactly one level under its type.
func assertFlatLayout(t *testing.T, root string) {
	t.Helper()

	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch len(parts) {
		case 1:
			assert.Equal(t, configKey, parts[0], "only the config lives at the store root")
		case 2:
			assert.Contains(t, []string{"data", "index", "keys", "locks", "snapshots"}, parts[0])
			assert.Len(t, parts[1], 64, "object names are 64 hex characters")
		default:
			assert.Failf(t, "unexpected nesting", "%s is more than one level under its type", rel)
		}
		return nil
	}))
}
