package docker

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
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

// Engine's own methods run against the api seam, so they are exercised with a
// mocked daemon — the coverage that the field being an injected interface buys.
func TestEngine_OpenMount(t *testing.T) {
	const (
		project = "demo"
		service = "db"
		target  = "/var/lib/postgresql/data"
	)
	matched := []container.Summary{{
		ID:     "pg-1",
		Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "demo_pgdata", Destination: target}},
	}}

	tests := []struct {
		name       string
		list       []container.Summary
		listErr    error
		wantErr    string
		wantStream string
	}{
		{name: "streams the matched container's mount", list: matched, wantStream: "tar-bytes"},
		{name: "no matching container is an error", list: []container.Summary{}, wantErr: "no container for service"},
		{name: "a list failure is wrapped", list: []container.Summary{}, listErr: errors.New("daemon down"), wantErr: "list containers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			api.On("ContainerList", mock.Anything, mock.Anything).Return(tt.list, tt.listErr)
			if tt.wantErr == "" {
				api.On("CopyFromContainer", mock.Anything, "pg-1", target).
					Return(io.NopCloser(strings.NewReader(tt.wantStream)), container.PathStat{}, nil)
			}
			e := &Engine{api: api}

			// Act
			rc, err := e.OpenMount(context.Background(), project, service, target)

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			defer rc.Close()
			got, err := io.ReadAll(rc)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStream, string(got))
		})
	}
}

func TestEngine_WriteMount(t *testing.T) {
	const (
		project = "demo"
		service = "db"
		target  = "/var/lib/postgresql/data"
	)

	t.Run("wipes then extracts into the mount's parent", func(t *testing.T) {
		// Arrange
		api := &mockAPI{}
		api.On("ContainerList", mock.Anything, mock.Anything).Return([]container.Summary{{
			ID:     "pg-1",
			Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "demo_pgdata", Destination: target}},
		}}, nil)
		expectImagePresent(api, wipeImage) // the wipe's own one-shot container
		expectOneShotRun(api, 0)
		api.On("CopyToContainer", mock.Anything, "pg-1", filepath.Dir(target), mock.Anything, mock.Anything).Return(nil)
		e := &Engine{api: api}

		// Act
		err := e.WriteMount(context.Background(), project, service, target, strings.NewReader("payload"))

		// Assert
		require.NoError(t, err)
		api.AssertExpectations(t)
		// The mount is wiped before the archive is extracted back into it.
		assert.Less(t, callIndex(api, "ContainerCreate"), callIndex(api, "CopyToContainer"),
			"the mount must be cleared before the restore stream is extracted")
	})

	t.Run("a missing service container is reported before any write", func(t *testing.T) {
		// Arrange
		api := &mockAPI{}
		api.On("ContainerList", mock.Anything, mock.Anything).Return([]container.Summary{}, nil)
		e := &Engine{api: api}

		// Act
		err := e.WriteMount(context.Background(), project, service, target, strings.NewReader("payload"))

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no container for service")
		api.AssertNotCalled(t, "CopyToContainer")
	})
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
