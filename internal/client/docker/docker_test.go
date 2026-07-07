package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingRun records the docker CLI arguments it is invoked with, so tests can
// assert on the assembled command without a daemon.
type capturingRun struct {
	calls [][]string
	err   error
}

func (c *capturingRun) run(_ context.Context, args ...string) error {
	c.calls = append(c.calls, args)
	return c.err
}

func TestComposeClientAssemblesArgs(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		act      func(c *ComposeClient) error
		wantArgs []string
	}{
		{
			name:     "stop with single file",
			files:    []string{"docker-compose.yml"},
			act:      func(c *ComposeClient) error { return c.Stop(context.Background()) },
			wantArgs: []string{"compose", "-f", "docker-compose.yml", "stop"},
		},
		{
			name:     "up interleaves multiple -f and bounds wait",
			files:    []string{"a.yml", "b.yml"},
			act:      func(c *ComposeClient) error { return c.Up(context.Background(), 30) },
			wantArgs: []string{"compose", "-f", "a.yml", "-f", "b.yml", "up", "-d", "--wait", "--wait-timeout", "30"},
		},
		{
			name:     "up falls back to default timeout when non-positive",
			files:    nil,
			act:      func(c *ComposeClient) error { return c.Up(context.Background(), 0) },
			wantArgs: []string{"compose", "up", "-d", "--wait", "--wait-timeout", "120"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			cap := &capturingRun{}
			c := NewComposeClient(tt.files...)
			c.run = cap.run

			// Act
			err := tt.act(c)

			// Assert
			require.NoError(t, err)
			require.Len(t, cap.calls, 1)
			assert.Equal(t, tt.wantArgs, cap.calls[0])
		})
	}
}

func TestMatchMount(t *testing.T) {
	summaries := []container.Summary{
		{ID: "other", Mounts: []container.MountPoint{
			{Type: mount.TypeVolume, Name: "demo_other", Destination: "/elsewhere"},
		}},
		{ID: "target", Mounts: []container.MountPoint{
			{Type: mount.TypeVolume, Name: "demo_pgdata", Destination: "/var/lib/postgresql/data"},
			{Type: mount.TypeBind, Source: "/host/cfg", Destination: "/etc/cfg"},
		}},
	}

	tests := []struct {
		name    string
		target  string
		wantOK  bool
		wantID  string
		wantMnt mountInfo
	}{
		{
			name:    "matches volume mount",
			target:  "/var/lib/postgresql/data",
			wantOK:  true,
			wantID:  "target",
			wantMnt: mountInfo{Type: "volume", Name: "demo_pgdata"},
		},
		{
			name:    "matches bind mount",
			target:  "/etc/cfg",
			wantOK:  true,
			wantID:  "target",
			wantMnt: mountInfo{Type: "bind", Source: "/host/cfg"},
		},
		{
			name:   "no container mounts target",
			target: "/nope",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			id, mnt, ok := matchMount(summaries, tt.target)

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantID, id)
				assert.Equal(t, tt.wantMnt, mnt)
			}
		})
	}
}

func TestWipeMount(t *testing.T) {
	tests := []struct {
		name     string
		mnt      mountInfo
		wantErr  string
		wantArgs []string
	}{
		{
			name:     "volume mounted by name",
			mnt:      mountInfo{Type: "volume", Name: "demo_pgdata"},
			wantArgs: []string{"run", "--rm", "-v", "demo_pgdata:/data", "busybox", "find", "/data", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "{}", "+"},
		},
		{
			name:     "bind mounted by host path",
			mnt:      mountInfo{Type: "bind", Source: "/host/cfg"},
			wantArgs: []string{"run", "--rm", "-v", "/host/cfg:/data", "busybox", "find", "/data", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "{}", "+"},
		},
		{
			name:    "unsupported type errors before running",
			mnt:     mountInfo{Type: "tmpfs"},
			wantErr: "unsupported mount type",
		},
		{
			name:    "empty source errors before running",
			mnt:     mountInfo{Type: "volume"},
			wantErr: "cannot resolve backing storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			cap := &capturingRun{}

			// Act
			err := wipeMount(context.Background(), tt.mnt, cap.run)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, cap.calls, "run must not be invoked on resolution failure")
				return
			}
			require.NoError(t, err)
			require.Len(t, cap.calls, 1)
			assert.Equal(t, tt.wantArgs, cap.calls[0])
		})
	}
}

func TestWipeMountPropagatesRunError(t *testing.T) {
	// Arrange
	cap := &capturingRun{err: errors.New("boom")}

	// Act
	err := wipeMount(context.Background(), mountInfo{Type: "volume", Name: "v"}, cap.run)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
