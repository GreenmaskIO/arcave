//go:build cdcstdio

package cdcstdio

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/require"
)

// TestCeiling measures a bare exec stream: 1 GiB up into cat, 1 GiB down out of one.
func TestCeiling(t *testing.T) {
	cli := newClient(t)
	ctx := t.Context()
	created, err := cli.ContainerCreate(ctx, &container.Config{Image: busyboxImage, Cmd: []string{"sleep", "600"}}, nil, nil, nil, "")
	require.NoError(t, err)
	defer cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	require.NoError(t, cli.ContainerStart(ctx, created.ID, container.StartOptions{}))
	const size = 1 << 30
	for _, c := range []struct {
		name string
		up   bool
		cmd  []string
	}{
		{"up cat", true, []string{"sh", "-c", "cat > /dev/null"}},
		{"up dd", true, []string{"sh", "-c", "dd of=/dev/null bs=1M 2>/dev/null"}},
		{"up cat|cat (2 hops)", true, []string{"sh", "-c", "cat | cat > /dev/null"}},
		{"down head", false, []string{"sh", "-c", "head -c 1073741824 /dev/zero"}},
		{"down head|cat", false, []string{"sh", "-c", "head -c 1073741824 /dev/zero | cat"}},
	} {
		exec, err := cli.ContainerExecCreate(ctx, created.ID, container.ExecOptions{Cmd: c.cmd, AttachStdin: c.up, AttachStdout: true, AttachStderr: true})
		require.NoError(t, err)
		h, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
		require.NoError(t, err)
		start := time.Now()
		if c.up {
			go func() {
				_, _ = io.CopyN(h.Conn, zeroReader{}, size)
				_ = h.CloseWrite()
			}()
		}
		n, _ := stdcopy.StdCopy(io.Discard, io.Discard, h.Reader)
		h.Close()
		d := time.Since(start)
		t.Logf("%-22s %4.0f MiB/s (%d bytes down)", c.name, float64(size>>20)/d.Seconds(), n)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
