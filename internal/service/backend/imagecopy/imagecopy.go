// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package imagecopy is the vocabulary two image backends stand on: where a capture
// lives, what it reaches the daemon through, and how it describes itself.
//
// The backends are its subpackages: fullcopy stores the save archive whole,
// dedupcopy splits it between a content-addressed blob store shared by every
// capture and that capture's own metadata.
package imagecopy

import (
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// imageStorage narrows a store root to the one image a savepoint holds for one
// replica. Both directions address it through here — the capture that writes the
// image and the restore that reads it back — so the gates stand here.
func imageStorage(
	store storages.Storager, snapshotID model.SavepointID, service string, replica int,
) (storages.Storager, error) {
	// "images" is a reserved key beside savepoint.json — no volume collides with it,
	// every volume ObjectID opens "volume-"; "replicas" names what follows it.
	const (
		imagesNamespace   = "images"
		replicasNamespace = "replicas"
	)

	if err := snapshotID.Validate(); err != nil {
		return nil, err
	}
	if service == "" {
		return nil, fmt.Errorf("cannot address the image of an unnamed service")
	}
	if replica < 1 {
		return nil, fmt.Errorf("service %s: replica number %d is not a replica", service, replica)
	}

	key := path.Join(snapshotID.String(), imagesNamespace, service, replicasNamespace, strconv.Itoa(replica))
	imageStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at image %s: %w", key, err)
	}
	return imageStore, nil
}

// upstreamImageStorage narrows a store root to one upstream image. It is keyed by
// content and belongs to no savepoint, so every savepoint naming the image shares
// one copy; its blobs go to the shared blob store like any capture's.
func upstreamImageStorage(store storages.Storager, imageID model.ImageDigestID) (storages.Storager, error) {
	const upstreamNamespace = "shared/oci/images"

	if _, err := model.ParseImageDigestID(imageID.String()); err != nil {
		return nil, err
	}
	algorithm, hex, _ := strings.Cut(imageID.String(), ":")
	key := path.Join(upstreamNamespace, algorithm, hex)
	imageStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at upstream image %s: %w", key, err)
	}
	return imageStore, nil
}

// ContainerImager is what a backend needs from the Docker engine: commit a stopped
// container and stream its image out, and load one back and name it.
type ContainerImager interface {
	CommitContainer(ctx context.Context, containerID, comment string, labels map[string]string) (model.ImageDigestID, error)
	SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error)
	RemoveImage(ctx context.Context, imageID model.ImageDigestID) error
	LoadImage(ctx context.Context, r io.Reader) error
	TagImage(ctx context.Context, imageID model.ImageDigestID, ref string) error
	ImageExists(ctx context.Context, imageID model.ImageDigestID) (bool, error)
}

// ArchiveLayout decides the shape an image takes in the store: one archive kept
// whole, or its members split between a shared blob store and this capture. It
// never speaks to the daemon — it is handed the archive as a stream and renders one
// back, and those two directions are the whole of its contract.
type ArchiveLayout interface {
	Store(ctx context.Context, dst storages.Storager, imageID model.ImageDigestID, archive io.Reader) (StoredSize, error)
	Load(ctx context.Context, src storages.Storager, imageID model.ImageDigestID, archive io.Writer) error
}

// Copier carries containers' layers in and out of one store root. Every exchange
// with the daemon is here; what the bytes look like once stored is the layout's.
type Copier struct {
	store  storages.Storager
	docker ContainerImager
	layout ArchiveLayout
}

// NewCopier binds a copier to the compose project store it works under and the layout its
// captures are written in.
func NewCopier(store storages.Storager, docker ContainerImager, layout ArchiveLayout) *Copier {
	return &Copier{
		store:  store,
		docker: docker,
		layout: layout,
	}
}

// Capture commits one stopped container's writable layer, stores it, and drops the
// committed image from the daemon. The commit is stamped with the container's
// upstream, so a container later rebuilt on this layer still names it.
func (c *Copier) Capture(ctx context.Context, req model.ImageCaptureRequest) (model.ImageCapture, error) {
	imageStore, err := imageStorage(c.store, req.SnapshotID, req.Container.Service, req.Container.Replica)
	if err != nil {
		return model.ImageCapture{}, err
	}
	comment := "arcave savepoint " + req.SnapshotID.String()
	imageID, err := c.docker.CommitContainer(ctx, req.Container.ID, comment, upstreamLabels(req.Container))
	if err != nil {
		return model.ImageCapture{}, err
	}

	size, err := c.storeArchive(ctx, imageStore, imageID)
	if err != nil {
		return model.ImageCapture{}, err
	}

	// Best-effort: the store is the source of truth, and a capture that is written
	// has succeeded whether or not the daemon's dangling copy could be dropped.
	if err := c.docker.RemoveImage(ctx, imageID); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("image_id", imageID.String()).
			Msg("committed image is stored but could not be removed from the daemon")
	}
	return model.ImageCapture{
		Service:          req.Container.Service,
		Replica:          req.Container.Replica,
		ContainerID:      req.Container.ID,
		CommittedImageID: imageID,
		UpstreamImageID:  req.Container.UpstreamImageID,
		UpstreamImageRef: req.Container.UpstreamImageRef,
		SizeBytes:        size.Image,
		StoredBytes:      size.Written,
	}, nil
}

// upstreamLabels are the labels a commit carries forward; none for what is
// unknown, so a layer never names an upstream it was not told.
func upstreamLabels(container model.ContainerRef) map[string]string {
	var labels map[string]string
	if container.UpstreamImageID != "" {
		labels = map[string]string{model.BaseImageIDLabel: container.UpstreamImageID.String()}
	}
	if container.UpstreamImageRef != "" {
		if labels == nil {
			labels = map[string]string{}
		}
		labels[model.BaseImageRefLabel] = container.UpstreamImageRef
	}
	return labels
}

// CaptureUpstream keeps one upstream image in the store, once for every savepoint
// naming it. An image neither the store nor the daemon holds, or one the daemon
// fails to hand over, is recorded by identity alone: a missing upstream must not
// cost the savepoint its data.
func (c *Copier) CaptureUpstream(
	ctx context.Context, req model.UpstreamImageCaptureRequest,
) (model.UpstreamImage, error) {
	image := req.Image
	image.Stored, image.StoredBytes = false, 0

	upstreamStore, err := upstreamImageStorage(c.store, image.ID)
	if err != nil {
		return model.UpstreamImage{}, err
	}
	stored, err := intactManifest(ctx, upstreamStore, image.ID)
	if err != nil {
		return model.UpstreamImage{}, err
	}
	if stored {
		image.Stored = true
		return image, nil
	}

	log := zerolog.Ctx(ctx).With().Str("upstream_image_id", image.ID.String()).Logger()
	if !req.InDaemon {
		log.Warn().Msg("the upstream image is in neither the store nor the daemon; only its identity is recorded")
		return image, nil
	}
	size, err := c.storeArchive(ctx, upstreamStore, image.ID)
	if err != nil {
		if ctx.Err() != nil {
			return model.UpstreamImage{}, err
		}
		log.Warn().Err(err).Msg("the upstream image could not be stored; only its identity is recorded")
		return image, nil
	}
	image.Stored = true
	image.StoredBytes = size.Written
	return image, nil
}

// storeArchive streams the committed image out of the daemon and hands it to the layout.
func (c *Copier) storeArchive(
	ctx context.Context, imageStore storages.Storager, imageID model.ImageDigestID,
) (StoredSize, error) {
	archive, err := c.docker.SaveImage(ctx, imageID)
	if err != nil {
		return StoredSize{}, err
	}
	defer func() {
		if err := archive.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive stream")
		}
	}()
	return c.layout.Store(ctx, imageStore, imageID, archive)
}

// Restore puts one recorded writable layer back into the daemon under req.ImageRef.
// An image the daemon already holds is only tagged: a load costs the whole archive,
// and the id in the record identifies it either way.
func (c *Copier) Restore(ctx context.Context, req model.ImageRestoreRequest) (model.ImageRestore, error) {
	imageStore, err := imageStorage(c.store, req.SnapshotID, req.Capture.Service, req.Capture.Replica)
	if err != nil {
		return model.ImageRestore{}, err
	}
	if req.ImageRef == "" {
		return model.ImageRestore{}, fmt.Errorf("service %s/%d: cannot restore an image under no reference",
			req.Capture.Service, req.Capture.Replica)
	}

	present, err := c.docker.ImageExists(ctx, req.Capture.CommittedImageID)
	if err != nil {
		return model.ImageRestore{}, err
	}
	if !present {
		if err := c.loadArchive(ctx, imageStore, req.Capture.CommittedImageID); err != nil {
			return model.ImageRestore{}, err
		}
	}

	if err := c.docker.TagImage(ctx, req.Capture.CommittedImageID, req.ImageRef); err != nil {
		return model.ImageRestore{}, err
	}
	return model.ImageRestore{
		Service:  req.Capture.Service,
		Replica:  req.Capture.Replica,
		ImageID:  req.Capture.CommittedImageID,
		ImageRef: req.ImageRef,
	}, nil
}

// loadArchive has the layout render the archive into a pipe the daemon reads from,
// then confirms the daemon holds the id the record names: a load resolving to
// another image must fail here and not at the rebuild.
func (c *Copier) loadArchive(
	ctx context.Context, imageStore storages.Storager, imageID model.ImageDigestID,
) error {
	pr, pw := io.Pipe()
	defer func() {
		if err := pr.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive pipe")
		}
	}()

	stored := make(chan error, 1)
	go func() {
		err := c.layout.Load(ctx, imageStore, imageID, pw)
		stored <- err
		// CloseWithError(nil) closes cleanly, so a layout that finished writing
		// reaches the reader as a clean EOF.
		//nolint:errcheck // the error travels to the reader
		pw.CloseWithError(err)
	}()

	if err := c.docker.LoadImage(ctx, pr); err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	// The daemon reports a failed load in-band and the reader never sees it, so the
	// layout's own error is the only account of why: prefer it over anything below.
	if err := <-stored; err != nil {
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
