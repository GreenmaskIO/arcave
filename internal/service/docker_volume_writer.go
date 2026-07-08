package service

import (
	"context"
	"io"

	"github.com/greenmaskio/gm-tools/internal/volume"
)

// dockerVolumeWriter is the write side of one Docker volume: it adapts a
// (client, project, service, target) tuple to a volume.Writer that wipes the
// mount and extracts a tar stream back into its container.
type dockerVolumeWriter struct {
	client  dockerVolumeClient
	project string
	service string
	target  string
}

var _ volume.Writer = dockerVolumeWriter{}

// newDockerVolumeWriter builds a volume.Writer over the given mount.
func newDockerVolumeWriter(client dockerVolumeClient, project, service, target string) dockerVolumeWriter {
	return dockerVolumeWriter{client: client, project: project, service: service, target: target}
}

func (w dockerVolumeWriter) Write(ctx context.Context, r io.Reader) error {
	return w.client.WriteMount(ctx, w.project, w.service, w.target, r)
}

func (w dockerVolumeWriter) DebugInfo() map[string]any {
	return map[string]any{"service": w.service, "target": w.target}
}
