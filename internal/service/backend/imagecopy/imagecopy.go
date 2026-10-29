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
	"github.com/greenmaskio/arcave/pkg/progress"
)

// "images" is a reserved key beside savepoint.json — no volume collides with it, every
// volume ObjectID opens "volume-"; "replicas" names what follows it.
const (
	namespaceImages   = "images"
	namespaceReplicas = "replicas"
)

// namespaceUpstream is where every image is kept, by content, under a compose project store.
const namespaceUpstream = "shared/oci/images"

// writableLayerStorage narrows a store root to the one writable layer a savepoint holds
// for one replica. Both directions address it through here — the capture that writes
// it and the restore that reads it back — so the gates stand here.
func writableLayerStorage(
	store storages.Navigator, snapshotID model.SavepointID, service string, replica int,
) (storages.Storager, error) {
	key, err := WritableLayerKey(snapshotID, service, replica)
	if err != nil {
		return nil, err
	}
	imageStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at image %s: %w", key, err)
	}
	return imageStore, nil
}

// imageStorage narrows a store root to one image. It is keyed by
// content and belongs to no savepoint, so every savepoint naming the image shares
// one copy; its blobs go to the shared blob store like any capture's.
func imageStorage(store storages.Navigator, imageID model.ImageDigestID) (storages.Storager, error) {
	key, err := ImageKey(imageID)
	if err != nil {
		return nil, err
	}
	imageStore, err := store.SubStorage(key, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at image %s: %w", key, err)
	}
	return imageStore, nil
}

// WritableLayerKey is where one replica's writable layer lives under a compose project store.
func WritableLayerKey(snapshotID model.SavepointID, service string, replica int) (string, error) {
	if err := snapshotID.Validate(); err != nil {
		return "", err
	}
	if service == "" {
		return "", fmt.Errorf("cannot address the image of an unnamed service")
	}
	if replica < 1 {
		return "", fmt.Errorf("service %s: replica number %d is not a replica", service, replica)
	}
	return path.Join(snapshotID.String(), namespaceImages, service, namespaceReplicas, strconv.Itoa(replica)), nil
}

// ImageKey is where one image lives under a compose project store.
func ImageKey(imageID model.ImageDigestID) (string, error) {
	if _, err := model.ParseImageDigestID(imageID.String()); err != nil {
		return "", err
	}
	algorithm, hex, _ := strings.Cut(imageID.String(), ":")
	return path.Join(namespaceUpstream, algorithm, hex), nil
}

// dockerImageEngine is the Docker engine's image half, as the copier needs it: commit a
// stopped container, stream an image out, load one back and name it.
type dockerImageEngine interface {
	CommitContainer(ctx context.Context, containerID, comment string, labels map[string]string) (model.ImageDigestID, error)
	SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error)
	RemoveImage(ctx context.Context, imageID model.ImageDigestID) error
	LoadImage(ctx context.Context, r io.Reader) error
	TagImage(ctx context.Context, imageID model.ImageDigestID, ref string) error
	ImageExists(ctx context.Context, imageID model.ImageDigestID) (bool, error)
}

// ImageArchiver stores an image's save archive and renders it back: kept whole, or
// its members split between a shared blob store and this capture. It never speaks to the daemon — it is handed the archive as a stream and renders one
// back, and those two directions are the whole of its contract.
type ImageArchiver interface {
	Store(ctx context.Context, dst storages.Writer, imageID model.ImageDigestID, archive io.Reader) (StoredSize, error)
	Load(ctx context.Context, src storages.Storager, imageID model.ImageDigestID, archive io.Writer) error
}

// Copier carries writable layers and images in and out of one store root. Every exchange
// with the daemon is here; what the bytes look like once stored is the archiver's.
type Copier struct {
	store                  storages.Navigator
	dockerImageEngine      dockerImageEngine
	writableLayerArchivers map[model.ImageBackendKind]ImageArchiver
	imageArchiver          ImageArchiver
	origin                 model.LayerOrigin
}

// NewCopier binds a copier to the compose project store it works under and the origin every
// commit is stamped with, the archiver of each backend a writable layer may name, and the one
// archiver every image is kept by.
func NewCopier(
	store storages.Navigator,
	origin model.LayerOrigin,
	dockerImageEngine dockerImageEngine,
	writableLayerArchivers map[model.ImageBackendKind]ImageArchiver,
	imageArchiver ImageArchiver,
) *Copier {
	return &Copier{
		store:                  store,
		dockerImageEngine:      dockerImageEngine,
		writableLayerArchivers: writableLayerArchivers,
		imageArchiver:          imageArchiver,
		origin:                 origin,
	}
}

// writableLayerArchiver returns the archiver of a writable layer backend; one the copier was not built with
// is refused before anything is committed or loaded.
func (s *Copier) writableLayerArchiver(kind model.ImageBackendKind) (ImageArchiver, error) {
	archiver, ok := s.writableLayerArchivers[kind]
	if !ok {
		return nil, fmt.Errorf("%w: image backend %q", model.ErrUnsupportedImageArchive, kind)
	}
	return archiver, nil
}

// CaptureWritableLayer commits one stopped container, stores the committed image and
// drops it from the daemon. The commit is stamped with the container's upstream, so a
// container later rebuilt on this layer still names it, and with the copier's origin, so a
// gc knows whose tag names it.
func (s *Copier) CaptureWritableLayer(ctx context.Context, req model.WritableLayerCaptureRequest) (model.WritableLayer, error) {
	archiver, err := s.writableLayerArchiver(req.Backend)
	if err != nil {
		return model.WritableLayer{}, err
	}
	imageStore, err := writableLayerStorage(s.store, req.SnapshotID, req.Container.Service, req.Container.Replica)
	if err != nil {
		return model.WritableLayer{}, err
	}
	comment := "arcave savepoint " + req.SnapshotID.String()
	imageID, err := s.dockerImageEngine.CommitContainer(ctx, req.Container.ID, comment, s.newLayerLabels(req.Container))
	if err != nil {
		return model.WritableLayer{}, err
	}

	size, err := s.storeImage(ctx, archiver, imageStore, imageID)
	if err != nil {
		return model.WritableLayer{}, err
	}

	// Best-effort: the store is the source of truth, and a capture that is written
	// has succeeded whether or not the daemon's dangling copy could be dropped.
	if err := s.dockerImageEngine.RemoveImage(ctx, imageID); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("image_id", imageID.String()).
			Msg("committed image is stored but could not be removed from the daemon")
	}
	return model.WritableLayer{
		Service:          req.Container.Service,
		Replica:          req.Container.Replica,
		ContainerID:      req.Container.ID,
		ContainerName:    req.Container.Name,
		CommittedImageID: imageID,
		ImageBackend:     req.Backend,
		UpstreamImageID:  req.Container.UpstreamImageID,
		UpstreamImageRef: req.Container.UpstreamImageRef,
		SizeBytes:        size.Image,
		StoredBytes:      size.Written,
	}, nil
}

// newLayerLabels are the labels a commit carries: the origin, and the upstream; none for an
// upstream that is unknown, so a layer never names one it was not told.
func (s *Copier) newLayerLabels(container model.ContainerRef) map[string]string {
	labels := s.origin.Labels()
	if container.UpstreamImageID != "" {
		labels[string(model.SystemLabelBaseImageID)] = container.UpstreamImageID.String()
	}
	if container.UpstreamImageRef != "" {
		labels[string(model.SystemLabelBaseImageRef)] = container.UpstreamImageRef
	}
	return labels
}

// CaptureImage keeps one image in the store, once for every savepoint
// naming it. An image neither the store nor the daemon holds, or one the daemon
// fails to hand over, is recorded by identity alone: a missing image must not
// cost the savepoint its data.
func (s *Copier) CaptureImage(
	ctx context.Context, req model.ImageCaptureRequest,
) (model.Image, error) {
	image := req.Image
	image.Stored, image.StoredBytes = false, 0

	upstreamStore, err := imageStorage(s.store, image.ID)
	if err != nil {
		return model.Image{}, err
	}
	stored, err := intactManifest(ctx, upstreamStore, image.ID)
	if err != nil {
		return model.Image{}, err
	}
	if stored {
		image.Stored = true
		return image, nil
	}

	log := zerolog.Ctx(ctx).With().Str("image_id", image.ID.String()).Logger()
	if !req.ExistsInDaemon {
		log.Warn().Msg("the image is in neither the store nor the daemon; only its identity is recorded")
		return image, nil
	}
	size, err := s.storeImage(ctx, s.imageArchiver, upstreamStore, image.ID)
	if err != nil {
		if ctx.Err() != nil {
			return model.Image{}, err
		}
		log.Warn().Err(err).Msg("the image could not be stored; only its identity is recorded")
		return image, nil
	}
	image.Stored = true
	image.StoredBytes = size.Written
	return image, nil
}

// storeImage streams one image out of the daemon and hands it to archiver.
func (s *Copier) storeImage(
	ctx context.Context, archiver ImageArchiver, imageStore storages.Writer, imageID model.ImageDigestID,
) (StoredSize, error) {
	archive, err := s.dockerImageEngine.SaveImage(ctx, imageID)
	if err != nil {
		return StoredSize{}, err
	}
	defer func() {
		if err := archive.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive stream")
		}
	}()
	return archiver.Store(ctx, imageStore, imageID, archive)
}

// RestoreWritableLayer loads one recorded writable layer's committed image back into the
// daemon and tags it req.Tag. An image the daemon already holds is only tagged: a load costs the whole archive,
// and the id in the record identifies it either way.
func (s *Copier) RestoreWritableLayer(ctx context.Context, req model.WritableLayerRestoreRequest) (model.WritableLayerRestoreResult, error) {
	archiver, err := s.writableLayerArchiver(req.Layer.ImageBackend)
	if err != nil {
		return model.WritableLayerRestoreResult{}, err
	}
	imageStore, err := writableLayerStorage(s.store, req.SnapshotID, req.Layer.Service, req.Layer.Replica)
	if err != nil {
		return model.WritableLayerRestoreResult{}, err
	}
	if req.Tag == "" {
		return model.WritableLayerRestoreResult{}, fmt.Errorf("service %s/%d: cannot restore an image under no reference",
			req.Layer.Service, req.Layer.Replica)
	}

	present, err := s.dockerImageEngine.ImageExists(ctx, req.Layer.CommittedImageID)
	if err != nil {
		return model.WritableLayerRestoreResult{}, err
	}
	if !present {
		if err := s.loadImage(ctx, archiver, imageStore, req.Layer.CommittedImageID, req.Layer.SizeBytes); err != nil {
			return model.WritableLayerRestoreResult{}, err
		}
	}

	if err := s.dockerImageEngine.TagImage(ctx, req.Layer.CommittedImageID, req.Tag); err != nil {
		return model.WritableLayerRestoreResult{}, err
	}
	return model.WritableLayerRestoreResult{
		Service:       req.Layer.Service,
		Replica:       req.Layer.Replica,
		ContainerName: req.Layer.ContainerName,
		ImageID:       req.Layer.CommittedImageID,
		Tag:           req.Tag,
	}, nil
}

// loadImage has archiver render the archive into a pipe the daemon reads from,
// counting it out of size, the archive size the capture recorded, then confirms
// the daemon holds the id the record names: a load resolving to another image
// must fail here and not at the rebuild.
func (s *Copier) loadImage(
	ctx context.Context, archiver ImageArchiver, imageStore storages.Storager, imageID model.ImageDigestID, size int64,
) error {
	pr, pw := io.Pipe()
	defer func() {
		if err := pr.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close image archive pipe")
		}
	}()

	stored := make(chan error, 1)
	go func() {
		err := archiver.Load(ctx, imageStore, imageID, progress.NewWriter(ctx, pw, size))
		stored <- err
		// CloseWithError(nil) closes cleanly, so an archiver that finished writing
		// reaches the reader as a clean EOF.
		//nolint:errcheck // the error travels to the reader
		pw.CloseWithError(err)
	}()

	if err := s.dockerImageEngine.LoadImage(ctx, pr); err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	// The daemon reports a failed load in-band and the reader never sees it, so the
	// archiver's own error is the only account of why: prefer it over anything below.
	if err := <-stored; err != nil {
		return err
	}

	present, err := s.dockerImageEngine.ImageExists(ctx, imageID)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("the loaded archive does not hold image %s", imageID)
	}
	return nil
}
