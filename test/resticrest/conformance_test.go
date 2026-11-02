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
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/greenmaskio/storages"
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

// relaySocketEnv names the unix socket this test binary relays its stdio to
// when restic starts it as its rclone program.
const relaySocketEnv = "ARCAVE_CONFORMANCE_RELAY_SOCKET"

// TestMain turns the test binary into the stdio transport's relay when restic
// runs it: stdin to the socket, the socket to stdout, until either side ends.
func TestMain(m *testing.M) {
	if socket := os.Getenv(relaySocketEnv); socket != "" {
		os.Exit(relay(socket))
	}
	os.Exit(m.Run())
}

// relay pipes stdin and stdout through the unix socket at path.
func relay(path string) int {
	conn, err := net.Dial("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.(*net.UnixConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	return 0
}

func TestConformsToRestic(t *testing.T) {
	tests := []struct {
		name string
		// serve serves root and returns the restic options and repository naming it.
		serve func(t *testing.T, root storages.Storager) (opts []string, repository string)
	}{
		{name: "tcp", serve: serveTCP},
		{name: "connserver", serve: serveConnServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: repository root → resolver → served endpoint. The store the
			// assertions read is the sub-root the ObjectID names — the very one the
			// resolver hands the protocol.
			resticBin := requireResticBinary(t)

			root, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, root.Close()) })
			store, err := root.SubStorage(conformanceObjectID, true)
			require.NoError(t, err)

			opts, repository := tt.serve(t, root)
			run := resticRunner(t, resticBin, repository, opts)
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
		})
	}
}

// serveTCP serves root on the loopback; the client here is a binary on the
// host, so the host it names is the loopback rather than a container's name for it.
func serveTCP(t *testing.T, root storages.Storager) ([]string, string) {
	t.Helper()

	srv := rest.NewEcho(t.Context(), repo.NewResolver(root))
	port, err := rest.Serve(srv, "")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Close()) })
	return nil, rest.RepositoryURLBase("", "127.0.0.1", port) + "/" + conformanceObjectID + "/"
}

// serveConnServer serves root over rest.ConnServer, one stream per restic run:
// restic starts this test binary as its rclone program, which relays its stdio
// to a unix socket served here. The docker exec bridge the stdio transport puts
// in between is test/cdcbackend's.
func serveConnServer(t *testing.T, root storages.Storager) ([]string, string) {
	t.Helper()

	// A short directory: a unix socket path is capped near 100 bytes.
	dir, err := os.MkdirTemp("", "arcave-relay")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	server := rest.NewConnServer(ctx, repo.NewResolver(root))
	var served sync.WaitGroup
	served.Add(1)
	go func() {
		defer served.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			served.Add(1)
			go func() {
				defer served.Done()
				_ = server.ServeRepository(ctx, conformanceObjectID, conn)
			}()
		}
	}()
	// Registered after root's Close, so it runs first: nothing serves root once it closes.
	t.Cleanup(func() {
		_ = ln.Close()
		cancel()
		served.Wait()
	})

	self, err := os.Executable()
	require.NoError(t, err)
	t.Setenv(relaySocketEnv, socket)
	return []string{"-o", "rclone.program=" + self}, "rclone:arcave"
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

// resticRunner returns a function running one restic command, after opts, against the served
// repository, failing the test with the command's own output.
//
// The connection count is left at restic's default of five: production runs one,
// but the chain must be concurrency-safe and this is the only test that says so.
func resticRunner(t *testing.T, bin, repository string, opts []string) func(*testing.T, ...string) string {
	t.Helper()

	return func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, append(slices.Clone(opts), args...)...)
		cmd.Env = append(os.Environ(),
			"RESTIC_REPOSITORY="+repository,
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
