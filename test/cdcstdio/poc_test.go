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

//go:build cdcstdio

// Package cdcstdio measures restic reaching its repository over a docker exec
// stream against the TCP path, on the raw Docker client. Run with
// `go test -tags cdcstdio ./test/cdcstdio/ -v`; CDCSTDIO_MIB sizes the volume,
// CDCSTDIO_TCP_HOST_NETWORK=1 runs the TCP baseline on the host network (a
// native Linux daemon) instead of host.docker.internal.
package cdcstdio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
)

const (
	resticImage  = "restic/restic:0.19.1"
	busyboxImage = "busybox"
	repoID       = "volume-named-poc"
	password     = "poc"
)

const stdioEntrypoint = `set -e
mkdir -p /run/arcave
mkfifo /run/arcave/req /run/arcave/resp
printf '%s\n' '#!/bin/sh' 'cat /run/arcave/resp 2>/dev/null &' 'exec cat > /run/arcave/req' > /run/arcave/relay
chmod +x /run/arcave/relay
exec restic "$@"`

var bridgeCmd = []string{"sh", "-c",
	"until [ -p /run/arcave/resp ]; do sleep 0.01; done; cat /run/arcave/req & exec cat > /run/arcave/resp"}

var stdioArgs = []string{"-o", "rclone.program=/run/arcave/relay"}

// transport runs one restic command against the repository and returns stdout.
type transport interface {
	name() string
	run(ctx context.Context, binds []string, args ...string) (string, error)
}

func TestPOC(t *testing.T) {
	cli := newClient(t)
	mib := 1024
	if v := os.Getenv("CDCSTDIO_MIB"); v != "" {
		var err error
		mib, err = strconv.Atoi(v)
		require.NoError(t, err)
	}

	src := newVolume(t, cli)
	busybox(t, cli, src, fmt.Sprintf(
		"dd if=/dev/urandom of=/data/blob bs=1M count=%d 2>/dev/null && mkdir -p /data/n && dd if=/dev/urandom of=/data/n/small bs=1k count=4 2>/dev/null", mib))
	before := hashDir(t, cli, src, "/data")

	for _, tr := range []transport{newStdio(t, cli), newTCP(t, cli)} {
		t.Run(tr.name(), func(t *testing.T) {
			// Each transport gets the source as captured, its own mutation after.
			vol := newVolume(t, cli)
			busybox2(t, cli, src, vol, "cp -a /a/. /b/")
			ctx := t.Context()
			binds := []string{vol + ":/data"}

			timed(t, "init", func() { mustRun(t, tr, ctx, nil, "init") })
			d := timed(t, "backup#1", func() { mustRun(t, tr, ctx, binds, "backup", "--json", "--host", "poc", "/data") })
			t.Logf("%s backup#1: %.1f MiB/s", tr.name(), float64(mib)/d.Seconds())
			busybox(t, cli, vol, fmt.Sprintf(
				"dd if=/dev/urandom of=/data/blob bs=1M count=%d seek=%d conv=notrunc 2>/dev/null", max(mib/20, 1), mib/3))
			mutated := hashDir(t, cli, vol, "/data")
			require.NotEqual(t, before, mutated)
			timed(t, "backup#2", func() { mustRun(t, tr, ctx, binds, "backup", "--json", "--host", "poc", "/data") })

			dst := newVolume(t, cli)
			d = timed(t, "restore", func() {
				mustRun(t, tr, ctx, []string{dst + ":/restore"}, "restore", "latest", "--target", "/restore")
			})
			t.Logf("%s restore: %.1f MiB/s", tr.name(), float64(mib)/d.Seconds())
			assert.Equal(t, mutated, hashDir(t, cli, dst, "/data/data"), "restore is byte-identical")

			out := ""
			timed(t, "snapshots", func() { out = mustRun(t, tr, ctx, nil, "snapshots", "--json") })
			assert.Equal(t, 2, strings.Count(out, `"short_id"`))
			timed(t, "forget --prune", func() { mustRun(t, tr, ctx, nil, "forget", "--keep-last", "1", "--prune") })
		})
	}
}

// A cancelled run removes its container and leaves no goroutine behind.
func TestPOC_CancelMidBackup(t *testing.T) {
	cli := newClient(t)
	tr := newStdio(t, cli)
	vol := newVolume(t, cli)
	busybox(t, cli, vol, "dd if=/dev/urandom of=/data/blob bs=1M count=512 2>/dev/null")
	mustRun(t, tr, t.Context(), nil, "init")
	goroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := tr.run(ctx, []string{vol + ":/data"}, "backup", "--json", "/data")

	require.Error(t, err)
	assert.Less(t, time.Since(start), 20*time.Second, "no hang")
	assert.Eventually(t, func() bool { return runtime.NumGoroutine() <= goroutines+2 }, 10*time.Second, 100*time.Millisecond,
		"goroutines: before %d, now %d", goroutines, runtime.NumGoroutine())
	assertNoOneShot(t, cli, start)
}

// restic killed mid-pack returns its exit, no hang.
func TestPOC_KillMidBackup(t *testing.T) {
	cli := newClient(t)
	tr := newStdio(t, cli)
	vol := newVolume(t, cli)
	busybox(t, cli, vol, "dd if=/dev/urandom of=/data/blob bs=1M count=512 2>/dev/null")
	mustRun(t, tr, t.Context(), nil, "init")
	tr.onStart = func(id string) {
		time.AfterFunc(2*time.Second, func() {
			_ = cli.ContainerKill(context.Background(), id, "KILL")
		})
	}
	start := time.Now()

	_, err := tr.run(t.Context(), []string{vol + ":/data"}, "backup", "--json", "/data")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 137")
	assert.Less(t, time.Since(start), 20*time.Second, "no hang")
	assertNoOneShot(t, cli, start)
}

// stdio is the design: restic as PID1, its repository over an exec bridge.
type stdio struct {
	cli     *client.Client
	server  *rest.ConnServer
	onStart func(id string)
}

func newStdio(t *testing.T, cli *client.Client) *stdio {
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	return &stdio{cli: cli, server: rest.NewConnServer(t.Context(), repo.NewResolver(store))}
}

func (s *stdio) name() string { return "stdio" }

func (s *stdio) run(ctx context.Context, binds []string, args ...string) (string, error) {
	created, err := s.cli.ContainerCreate(ctx, &container.Config{
		Image:        resticImage,
		Entrypoint:   []string{"sh", "-c", stdioEntrypoint, "restic"},
		Cmd:          append(append(append([]string{}, stdioArgs...), strings.Fields(os.Getenv("CDCSTDIO_EXTRA_ARGS"))...), args...),
		Env:          []string{"RESTIC_REPOSITORY=rclone:arcave", "RESTIC_PASSWORD=" + password},
		Labels:       map[string]string{"arcave.oneshot": "true"},
		AttachStdout: true,
		AttachStderr: true,
	}, &container.HostConfig{Binds: binds}, nil, nil, "")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = s.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	attached, err := s.cli.ContainerAttach(ctx, created.ID, container.AttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()
	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", err
	}
	if s.onStart != nil {
		s.onStart(created.ID)
	}

	var bridgeErr bytes.Buffer
	served := make(chan error, 1)
	exec, err := s.cli.ContainerExecCreate(ctx, created.ID, container.ExecOptions{
		Cmd: bridgeCmd, AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	var stream *execStream
	if err == nil {
		var hijacked types.HijackedResponse
		hijacked, err = s.cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
		if err == nil {
			stream = newExecStream(hijacked, &bridgeErr)
			go func() { served <- s.server.ServeRepository(ctx, repoID, stream) }()
		}
	}
	execErr := err

	var stdout, stderr bytes.Buffer
	_, copyErr := stdcopy.StdCopy(&stdout, &stderr, attached.Reader)
	statusCh, errCh := s.cli.ContainerWait(context.WithoutCancel(ctx), created.ID, container.WaitConditionNotRunning)
	var code int64
	select {
	case err := <-errCh:
		return "", err
	case st := <-statusCh:
		code = st.StatusCode
	}
	if stream != nil {
		_ = stream.Close()
		if err := <-served; err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		}
	}
	if code != 0 {
		return stdout.String(), fmt.Errorf("restic %v: exit %d: %s%s", args, code, stderr.String(), bridgeErr.String())
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if execErr != nil {
		return "", execErr
	}
	if copyErr != nil {
		return "", copyErr
	}
	return stdout.String(), nil
}

// execStream is an exec's stdio as one stream: stdout read, stdin written.
type execStream struct {
	hijacked types.HijackedResponse
	pr       *io.PipeReader
	once     sync.Once
}

func newExecStream(hijacked types.HijackedResponse, stderr io.Writer) *execStream {
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, stderr, hijacked.Reader)
		pw.CloseWithError(err)
	}()
	return &execStream{hijacked: hijacked, pr: pr}
}

func (s *execStream) Read(p []byte) (int, error)  { return s.pr.Read(p) }
func (s *execStream) Write(p []byte) (int, error) { return s.hijacked.Conn.Write(p) }
func (s *execStream) Close() error {
	s.once.Do(func() {
		_ = s.hijacked.CloseWrite()
		s.hijacked.Close()
		_ = s.pr.Close()
	})
	return nil
}

// tcp is today's path: rest.Serve on the loopback, restic dialling it.
type tcp struct {
	cli         *client.Client
	url         string
	hostNetwork bool
}

func newTCP(t *testing.T, cli *client.Client) *tcp {
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	e := rest.NewEcho(t.Context(), repo.NewResolver(store))
	port, err := rest.Serve(e, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	hostNetwork := os.Getenv("CDCSTDIO_TCP_HOST_NETWORK") == "1"
	host := "host.docker.internal"
	if hostNetwork {
		host = "127.0.0.1"
	}
	return &tcp{cli: cli, url: rest.RepositoryURLBase("http", host, port) + "/" + repoID + "/", hostNetwork: hostNetwork}
}

func (s *tcp) name() string { return "tcp" }

func (s *tcp) run(ctx context.Context, binds []string, args ...string) (string, error) {
	host := &container.HostConfig{Binds: binds}
	if s.hostNetwork {
		host.NetworkMode = network.NetworkHost
	}
	return runToExit(ctx, s.cli, &container.Config{
		Image: resticImage, Cmd: args,
		Env:          []string{"RESTIC_REPOSITORY=" + s.url, "RESTIC_PASSWORD=" + password},
		AttachStdout: true, AttachStderr: true,
	}, host)
}

func runToExit(ctx context.Context, cli *client.Client, cfg *container.Config, host *container.HostConfig) (string, error) {
	created, err := cli.ContainerCreate(ctx, cfg, host, nil, nil, "")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	attached, err := cli.ContainerAttach(ctx, created.ID, container.AttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, attached.Reader)
	statusCh, errCh := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return "", err
	case st := <-statusCh:
		if st.StatusCode != 0 {
			return "", fmt.Errorf("%v: exit %d: %s", cfg.Cmd, st.StatusCode, stderr.String())
		}
	}
	return stdout.String(), nil
}

func mustRun(t *testing.T, tr transport, ctx context.Context, binds []string, args ...string) string {
	t.Helper()
	out, err := tr.run(ctx, binds, args...)
	require.NoError(t, err)
	return out
}

func timed(t *testing.T, what string, fn func()) time.Duration {
	t.Helper()
	start := time.Now()
	fn()
	d := time.Since(start)
	t.Logf("%-16s %s", what, d.Round(10*time.Millisecond))
	return d
}

func newClient(t *testing.T) *client.Client {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	if _, err := cli.Ping(context.Background()); err != nil {
		t.Skip("no reachable docker daemon: " + err.Error())
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func newVolume(t *testing.T, cli *client.Client) string {
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "arcave-cdcstdio-" + hex.EncodeToString(suffix)
	_, err := cli.VolumeCreate(context.Background(), volume.CreateOptions{Name: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.VolumeRemove(context.Background(), name, true) })
	return name
}

func busybox(t *testing.T, cli *client.Client, vol, script string) string {
	t.Helper()
	out, err := runToExit(t.Context(), cli, &container.Config{
		Image: busyboxImage, Cmd: []string{"sh", "-c", script}, AttachStdout: true, AttachStderr: true,
	}, &container.HostConfig{Binds: []string{vol + ":/data"}})
	require.NoError(t, err)
	return out
}

func busybox2(t *testing.T, cli *client.Client, a, b, script string) {
	t.Helper()
	_, err := runToExit(t.Context(), cli, &container.Config{
		Image: busyboxImage, Cmd: []string{"sh", "-c", script}, AttachStdout: true, AttachStderr: true,
	}, &container.HostConfig{Binds: []string{a + ":/a", b + ":/b"}})
	require.NoError(t, err)
}

func hashDir(t *testing.T, cli *client.Client, vol, dir string) string {
	t.Helper()
	sums := busybox(t, cli, vol, "cd "+dir+" && find . -type f -exec sha256sum {} + | sort")
	require.NotEmpty(t, sums)
	return sums
}

func assertNoOneShot(t *testing.T, cli *client.Client, since time.Time) {
	t.Helper()
	assert.Eventually(t, func() bool {
		list, err := cli.ContainerList(context.Background(), container.ListOptions{All: true})
		require.NoError(t, err)
		for _, c := range list {
			if c.Image == resticImage && c.Labels["arcave.oneshot"] == "true" && c.Created >= since.Unix() {
				return false
			}
		}
		return true
	}, 10*time.Second, 200*time.Millisecond, "the one-shot container is removed")
}
