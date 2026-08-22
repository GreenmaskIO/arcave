// Package imagecopy moves a container's writable layer between the daemon and
// the store, wholesale in both directions: one committed image, saved verbatim,
// per container per savepoint, and loaded back under a tag of its own.
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

var errNoStore = errors.New("image copier has no store")

// ContainerImager is what the copier needs from the Docker engine: commit a
// stopped container and stream its image out, and load one back and name it.
type ContainerImager interface {
	CommitContainer(ctx context.Context, containerID string) (string, error)
	SaveImage(ctx context.Context, imageID string) (io.ReadCloser, error)
	RemoveImage(ctx context.Context, imageID string) error
	LoadImage(ctx context.Context, r io.Reader) error
	TagImage(ctx context.Context, imageID, ref string) error
	ImageExists(ctx context.Context, imageID string) (bool, error)
}

// Copier carries containers' layers in and out of one store root.
type Copier struct {
	store  storages.Storager
	docker ContainerImager
}

// New binds the copier to the stack store it works under.
func New(store storages.Storager, docker ContainerImager) *Copier {
	return &Copier{store: store, docker: docker}
}

// Capture commits one stopped container's writable layer, stores the image
// verbatim, and drops the committed image from the daemon.
func (c *Copier) Capture(ctx context.Context, req model.ImageCaptureRequest) (model.ImageCapture, error) {
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

// Restore puts one recorded writable layer back into the daemon and names it
// req.ImageRef. An image the daemon already holds is only tagged: a load costs the
// whole archive, and the id in the record is what identifies it either way.
func (c *Copier) Restore(ctx context.Context, req model.ImageRestoreRequest) (model.ImageRestore, error) {
	capture, err := c.captureStore(model.ImageCaptureRequest{
		SnapshotID: req.SnapshotID,
		Container: model.ContainerRef{
			Service: req.Capture.Service, Replica: req.Capture.Replica, ID: req.Capture.ContainerID,
		},
	})
	if err != nil {
		return model.ImageRestore{}, err
	}
	if req.ImageRef == "" {
		return model.ImageRestore{}, fmt.Errorf("service %s/%d: cannot restore an image under no reference",
			req.Capture.Service, req.Capture.Replica)
	}

	present, err := c.docker.ImageExists(ctx, req.Capture.ImageID)
	if err != nil {
		return model.ImageRestore{}, err
	}
	if !present {
		if err := c.loadImage(ctx, capture, req.Capture.ImageID); err != nil {
			return model.ImageRestore{}, err
		}
	}

	if err := c.docker.TagImage(ctx, req.Capture.ImageID, req.ImageRef); err != nil {
		return model.ImageRestore{}, err
	}
	return model.ImageRestore{
		Service:  req.Capture.Service,
		Replica:  req.Capture.Replica,
		ImageID:  req.Capture.ImageID,
		ImageRef: req.ImageRef,
	}, nil
}

// storeImage streams the image out of the daemon into the capture and reports how
// many bytes were stored.
func (c *Copier) storeImage(ctx context.Context, capture storages.Storager, imageID string) (int64, error) {
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

// loadImage streams the stored archive into the daemon and confirms it answers to
// the id the record names: a load resolving to another image must fail here and
// not at the rebuild.
func (c *Copier) loadImage(ctx context.Context, capture storages.Storager, imageID string) error {
	src, err := capture.GetObject(ctx, archiveName)
	if err != nil {
		if errors.Is(err, storages.ErrFileNotFound) {
			return fmt.Errorf("%w: image %s: %w", model.ErrSavepointImageMissing, imageID, err)
		}
		return fmt.Errorf("read image archive: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive stream")
		}
	}()

	if err := c.docker.LoadImage(ctx, src); err != nil {
		return err
	}

	present, err := c.docker.ImageExists(ctx, imageID)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("the loaded archive does not hold image %s", imageID)
	}
	return nil
}

// captureStore narrows the root to the one capture a request addresses; it is the
// only place a request becomes a storage key, so the gates stand here.
func (c *Copier) captureStore(req model.ImageCaptureRequest) (storages.Storager, error) {
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
