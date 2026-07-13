package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestMatchMount(t *testing.T) {
	summaries := []container.Summary{
		{
			ID: "other", Mounts: []container.MountPoint{
				{
					Type:        mount.TypeVolume,
					Name:        "demo_other",
					Destination: "/elsewhere",
				},
			}},
		{
			ID: "target", Mounts: []container.MountPoint{
				{
					Type:        mount.TypeVolume,
					Name:        "demo_pgdata",
					Destination: "/var/lib/postgresql/data",
				},
				{
					Type:        mount.TypeBind,
					Source:      "/host/cfg",
					Destination: "/etc/cfg",
				},
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
			runner := &mockRunner{}
			if tt.wantErr == "" {
				runner.On("run", mock.Anything, tt.wantArgs).Return(nil).Once()
			}

			// Act
			err := wipeMount(context.Background(), tt.mnt, runner.run)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				runner.AssertNotCalled(t, "run")
				return
			}
			require.NoError(t, err)
			runner.AssertExpectations(t)
		})
	}
}

func TestWipeMount_PropagatesRunError(t *testing.T) {
	// Arrange
	runner := &mockRunner{}
	runner.On("run", mock.Anything, mock.Anything).Return(errors.New("boom"))

	// Act
	err := wipeMount(context.Background(), mountInfo{Type: "volume", Name: "v"}, runner.run)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
