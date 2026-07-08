package service

import (
	"context"
	"io"

	"github.com/greenmaskio/gm-tools/internal/volume"
)

// dockerVolumeReader is the read side of one Docker volume: it adapts a
// (client, project, service, target) tuple to a volume.Reader that streams the
// mount's current contents out of its container as a tar archive. It lives here
// rather than in the docker package to avoid a docker→service import cycle over
// volume.Reader.
type dockerVolumeReader struct {
	client  dockerVolumeClient
	project string
	service string
	target  string
}

var _ volume.Reader = dockerVolumeReader{}

// newDockerVolumeReader builds a volume.Reader over the given mount.
func newDockerVolumeReader(client dockerVolumeClient, project, service, target string) dockerVolumeReader {
	return dockerVolumeReader{client: client, project: project, service: service, target: target}
}

func (r dockerVolumeReader) Open(ctx context.Context) (io.ReadCloser, error) {
	return r.client.OpenMount(ctx, r.project, r.service, r.target)
}

func (r dockerVolumeReader) DebugInfo() map[string]any {
	return map[string]any{"service": r.service, "target": r.target}
}
