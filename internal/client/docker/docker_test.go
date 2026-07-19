package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/stretchr/testify/assert"
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
		name      string
		mnt       mountInfo
		wantErr   string
		wantBinds []string
	}{
		{
			name:      "volume mounted by name",
			mnt:       mountInfo{Type: "volume", Name: "demo_pgdata"},
			wantBinds: []string{"demo_pgdata:/data"},
		},
		{
			name:      "bind mounted by host path",
			mnt:       mountInfo{Type: "bind", Source: "/host/cfg"},
			wantBinds: []string{"/host/cfg:/data"},
		},
		{
			name:    "unsupported type errors before reaching the daemon",
			mnt:     mountInfo{Type: "tmpfs"},
			wantErr: "unsupported mount type",
		},
		{
			name:    "empty source errors before reaching the daemon",
			mnt:     mountInfo{Type: "volume"},
			wantErr: "cannot resolve backing storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			if tt.wantErr == "" {
				expectImagePresent(api, wipeImage)
				expectOneShotRun(api, 0)
			}

			// Act
			err := wipeMount(context.Background(), api, tt.mnt)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				api.AssertNotCalled(t, "ContainerCreate")
				return
			}
			require.NoError(t, err)
			api.AssertExpectations(t)
			cfg := createdConfig(t, api)
			host := api.Calls[callIndex(api, "ContainerCreate")].Arguments.Get(2).(*container.HostConfig)
			assert.Equal(t, wipeImage, cfg.Image)
			assert.EqualValues(t, []string{"find", "/data", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "{}", "+"}, cfg.Cmd)
			assert.Equal(t, tt.wantBinds, host.Binds)
		})
	}
}
