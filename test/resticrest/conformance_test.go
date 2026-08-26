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

// Package resticrest drives the restic REST endpoint the way its clients do —
// a restic binary on the host, a stock restic container over the loopback — so
// nothing here is a unit of the packages it wires: the echo app, the repository
// registry and the repositories behind them, over a real listener.
//
// It lives outside those packages for that reason, beside the other integration
// tests. Guarded twice, per test: -short skips, and so does a host with no
// restic and no reachable daemon.
package resticrest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
)

// rest's own tests prove the routes speak restic's protocol over a bare
// httptest recorder. This one proves that a real listener does not change that
// answer: the same lifecycle, over the echo app and the repository registry,
// where a trailing slash swallowed on the way in, a HEAD answered as a GET or a
// Range dropped would each break restic quietly.
//
// Guarded like every other restic test: -short skips it, and so does a host with
// no restic binary.

// conformancePassword opens the repository this test creates and throws away.
const conformancePassword = "arcave-resticserver-conformance"

// conformanceObjectID is an arcave ObjectID of the shape the cdc backend serves
// a repository under — one path segment, and the name of its directory.
const conformanceObjectID = "volume-named-pgdata"

func TestConformsToRestic(t *testing.T) {
	// Arrange: repository root → resolver → served endpoint, addressed on the
	// loopback, since the client here is a binary on the host rather than a
	// container. The store the assertions read is the sub-root the ObjectID names
	// — the very one the resolver hands the protocol.
	resticBin := requireResticBinary(t)

	root, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	store, err := root.SubStorage(conformanceObjectID, true)
	require.NoError(t, err)

	srv := rest.NewEcho(t.Context(), repo.NewResolver(root))
	port, err := rest.Serve(srv, "")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Close()) })

	// The client here is a binary on the host, so the host it names is the
	// loopback rather than a container's name for it.
	repoURL := rest.RepositoryURLBase("", "127.0.0.1", port) + "/" + conformanceObjectID + "/"
	run := resticRunner(t, resticBin, repoURL)
	fixture := newConformanceFixture(t)

	// Act & Assert: one lifecycle, each step gated on the previous one.
	run(t, "init")
	exists, err := store.Exists(context.Background(), "config")
	require.NoError(t, err)
	require.True(t, exists, "init must land the repository config in the store")

	run(t, "backup", fixture)
	require.Contains(t, run(t, "snapshots", "--json"), `"id"`, "the first backup produced no snapshot")

	// A second backup over changed data, so forget has something to drop and
	// prune has something to repack — the DELETEs of the protocol.
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "changing.txt"),
		[]byte(strings.Repeat("second", 4096)), 0o600))
	run(t, "backup", fixture)

	// --read-data reads every pack back through the ranged GETs.
	run(t, "check", "--read-data")

	target := t.TempDir()
	run(t, "restore", "latest", "--target", target)

	run(t, "forget", "--keep-last", "1", "--prune")
	run(t, "check", "--read-data")
}

// requireResticBinary skips unless a real restic can be exercised.
func requireResticBinary(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping the data plane conformance test in short mode")
	}
	path, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("skipping the data plane conformance test: no restic binary on PATH")
	}
	return path
}

// resticRunner returns a function running one restic command against the served
// repository, failing the test with the command's own output.
//
// The connection count is left at restic's default of five: production runs one,
// but the chain must be concurrency-safe and this is the only test that says so.
func resticRunner(t *testing.T, bin, repoURL string) func(*testing.T, ...string) string {
	t.Helper()

	return func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = append(os.Environ(),
			"RESTIC_REPOSITORY="+repoURL,
			"RESTIC_PASSWORD="+conformancePassword,
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "restic %s failed:\n%s", strings.Join(args, " "), out)
		return string(out)
	}
}

// newConformanceFixture writes the directory restic backs up: small files and
// one large enough to span more than a single blob, so packs and ranged reads
// are both exercised.
func newConformanceFixture(t *testing.T) string {
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
