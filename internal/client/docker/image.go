package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

// CommitContainer snapshots a stopped container's writable layer as a new,
// untagged image and returns its id.
func (e *Engine) CommitContainer(ctx context.Context, containerID string) (string, error) {
	// Unpaused: the caller has already stopped the services, and a stopped container
	// cannot be paused.
	resp, err := e.api.ContainerCommit(ctx, containerID, container.CommitOptions{Pause: false})
	if err != nil {
		return "", fmt.Errorf("commit container %s: %w", containerID, err)
	}
	return resp.ID, nil
}

// SaveImage streams one image out of the daemon as a save archive. The caller
// owns the stream and closes it.
func (e *Engine) SaveImage(ctx context.Context, imageID string) (io.ReadCloser, error) {
	src, err := e.api.ImageSave(ctx, []string{imageID})
	if err != nil {
		return nil, fmt.Errorf("save image %s: %w", imageID, err)
	}
	return src, nil
}

// RemoveImage drops one image from the daemon. Not forced, so an image something
// still references is refused rather than taken.
func (e *Engine) RemoveImage(ctx context.Context, imageID string) error {
	if _, err := e.api.ImageRemove(ctx, imageID, image.RemoveOptions{}); err != nil {
		return fmt.Errorf("remove image %s: %w", imageID, err)
	}
	return nil
}
