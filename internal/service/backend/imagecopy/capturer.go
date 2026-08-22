// Package imagecopy stores a stopped container's writable layer wholesale: one
// committed image, saved verbatim, per container per savepoint.
package imagecopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// ImagesNamespace is the directory image captures live in — a reserved key beside
// savepoint.json; no volume collides with it, every volume ObjectID opens "volume-".
const ImagesNamespace = "images"

const archiveName = "image.tar"

var errNoStore = errors.New("image capturer has no store")

// ContainerImager is what the capturer needs from the Docker engine: commit a
// stopped container, stream the image out, drop it again.
type ContainerImager interface {
	CommitContainer(ctx context.Context, containerID string) (string, error)
	SaveImage(ctx context.Context, imageID string) (io.ReadCloser, error)
	RemoveImage(ctx context.Context, imageID string) error
}

// Capturer commits containers into one store root.
type Capturer struct {
	store  storages.Storager
	docker ContainerImager
}

// New binds the capturer to the stack store it writes under.
func New(store storages.Storager, docker ContainerImager) *Capturer {
	return &Capturer{store: store, docker: docker}
}

// Capture commits one stopped container's writable layer, stores the image
// verbatim, and drops the committed image from the daemon.
func (c *Capturer) Capture(ctx context.Context, req model.ImageCaptureRequest) (model.ImageCapture, error) {
	capture, err := c.captureStore(req)
	if err != nil {
		return model.ImageCapture{}, err
	}

	imageID, err := c.docker.CommitContainer(ctx, req.Container.ID)
	if err != nil {
		return model.ImageCapture{}, err
	}

	size, err := c.storeImage(ctx, capture, imageID)
	if err != nil {
		return model.ImageCapture{}, err
	}

	if err := c.docker.RemoveImage(ctx, imageID); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("image_id", imageID).
			Msg("committed image is stored but could not be removed from the daemon")
	}

	return model.ImageCapture{
		Service:     req.Container.Service,
		Replica:     req.Container.Replica,
		ContainerID: req.Container.ID,
		ImageID:     imageID,
		BaseImage:   req.Container.BaseImage,
		SizeBytes:   size,
	}, nil
}

// storeImage streams the image out of the daemon into the capture and reports how
// many bytes were stored.
func (c *Capturer) storeImage(ctx context.Context, capture storages.Storager, imageID string) (int64, error) {
	src, err := c.docker.SaveImage(ctx, imageID)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive stream")
		}
	}()

	counted := &countingReader{r: src}
	if err := capture.PutObject(ctx, archiveName, counted); err != nil {
		return 0, fmt.Errorf("write image archive: %w", err)
	}
	return counted.n, nil
}

// captureStore narrows the root to the one capture a request addresses; it is the
// only place a request becomes a storage key, so the gates stand here.
func (c *Capturer) captureStore(req model.ImageCaptureRequest) (storages.Storager, error) {
	if c.store == nil {
		return nil, errNoStore
	}
	if err := req.SnapshotID.Validate(); err != nil {
		return nil, err
	}
	if req.Container.Service == "" {
		return nil, fmt.Errorf("cannot capture a container of an unnamed service")
	}
	if req.Container.Replica < 1 {
		return nil, fmt.Errorf("service %s: replica number %d is not a replica",
			req.Container.Service, req.Container.Replica)
	}

	key := path.Join(req.SnapshotID.String(), ImagesNamespace,
		req.Container.Service, strconv.Itoa(req.Container.Replica))
	capture, err := c.store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at image capture %s: %w", key, err)
	}
	return capture, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
