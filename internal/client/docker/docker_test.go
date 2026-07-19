package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
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
				expectImagePresent(api)
				expectWipeRun(api, 0)
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
			cfg := api.Calls[1].Arguments.Get(1).(*container.Config)
			host := api.Calls[1].Arguments.Get(2).(*container.HostConfig)
			assert.Equal(t, wipeImage, cfg.Image)
			assert.Equal(t, tt.wantBinds, host.Binds)
		})
	}
}

// The image is pulled only when the daemon does not already have it, and the
// pull stream is drained: the daemon does not finish a pull nobody reads.
func TestWipeMount_PullsMissingImage(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("ImageInspect", mock.Anything, wipeImage).
		Return(image.InspectResponse{}, cerrdefs.ErrNotFound)
	pulled := &countingReader{ReadCloser: io.NopCloser(strings.NewReader(`{"status":"done"}`))}
	api.On("ImagePull", mock.Anything, wipeImage, image.PullOptions{}).Return(io.ReadCloser(pulled), nil)
	expectWipeRun(api, 0)

	// Act
	err := wipeMount(context.Background(), api, mountInfo{Type: "volume", Name: "v"})

	// Assert
	require.NoError(t, err)
	api.AssertExpectations(t)
	assert.True(t, pulled.drained, "the pull stream must be read to completion")
}

// A non-zero exit carries the container's own output, the way the CLI's
// combined output used to.
func TestWipeMount_NonZeroExitCarriesLogs(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api)
	expectWipeRun(api, 137)
	api.On("ContainerLogs", mock.Anything, "wipe-1", mock.Anything).
		Return(io.NopCloser(strings.NewReader("rm: permission denied\n")), nil)

	// Act
	err := wipeMount(context.Background(), api, mountInfo{Type: "volume", Name: "v"})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 137")
	assert.Contains(t, err.Error(), "rm: permission denied")
}

func TestWipeMount_PropagatesCreateError(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	expectImagePresent(api)
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{}, errors.New("boom"))

	// Act
	err := wipeMount(context.Background(), api, mountInfo{Type: "volume", Name: "v"})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// expectImagePresent makes the daemon report the wipe image as already pulled.
func expectImagePresent(api *mockAPI) {
	api.On("ImageInspect", mock.Anything, wipeImage).Return(image.InspectResponse{}, nil)
}

// expectWipeRun drives one throwaway container through to the given exit status.
func expectWipeRun(api *mockAPI, exitCode int64) {
	api.On("ContainerCreate", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(container.CreateResponse{ID: "wipe-1"}, nil)
	api.On("ContainerStart", mock.Anything, "wipe-1", container.StartOptions{}).Return(nil)

	status := make(chan container.WaitResponse, 1)
	status <- container.WaitResponse{StatusCode: exitCode}
	api.On("ContainerWait", mock.Anything, "wipe-1", container.WaitConditionNotRunning).
		Return((<-chan container.WaitResponse)(status), (<-chan error)(make(chan error)))

	api.On("ContainerRemove", mock.Anything, "wipe-1", container.RemoveOptions{Force: true}).Return(nil)
}

// countingReader records whether its stream was read to EOF.
type countingReader struct {
	io.ReadCloser
	drained bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		c.drained = true
	}
	return n, err
}
