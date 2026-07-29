// Package tarcopy captures a volume wholesale: one tar archive per volume per
// snapshot, under <snapshotID>/<objectID>/, streamed through the Docker engine
// and stored verbatim so ownership and permissions survive a restore.
//
// Wholesale is what the name carries: the whole volume in one archive, as
// against cdc, which writes only the chunks that changed.
package tarcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// archiveName is the one object a capture consists of.
const archiveName = "data.tar"

// Payload is a capture's SnapshotStat payload. The size_bytes json tag matches
// the old core's records, so they read back without a migration.
type Payload struct {
	SizeBytes int64 `json:"size_bytes"`
}

// MountStreamer is what the backend needs from the Docker engine: OpenMount
// streams a mount's contents out, WriteMount wipes the mount and extracts a
// stream back in. The concrete *docker.Engine satisfies it.
type MountStreamer interface {
	OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error)
	WriteMount(ctx context.Context, project, service, target string, r io.Reader) error
}

// Factory builds tarcopy snapshotters over one shared Docker engine.
type Factory struct {
	docker MountStreamer
}

// NewFactory builds the factory. Not New, so that building off it does not read
// New(docker).New(spec) at every call site.
func NewFactory(docker MountStreamer) *Factory { return &Factory{docker: docker} }

func (*Factory) Kind() model.SnapshotBackendKind { return model.SnapshotBackendCopy }

// New resolves both things fixed for an instance's life: the root the tier names,
// and the mount the volume's data is reached through. So a volume with no way in
// fails here, at registration, rather than at the first capture.
func (f *Factory) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	store, err := spec.TierStore()
	if err != nil {
		return nil, err
	}
	mount, err := captureMount(spec.Volume)
	if err != nil {
		return nil, err
	}
	return Snapshotter{volume: spec.Volume, mount: mount, store: store, docker: f.docker}, nil
}

// Snapshotter captures one volume into one tier's root.
type Snapshotter struct {
	volume model.Volume
	mount  captureMountRef
	store  storages.Storager
	docker MountStreamer
}

func (s Snapshotter) Volume() model.Volume { return s.volume }

// Snapshot streams the volume out through its capture mount and stores it
// verbatim.
func (s Snapshotter) Snapshot(
	ctx context.Context, req model.BackendSnapshotRequest,
) (model.SnapshotStat, error) {
	capture, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return model.SnapshotStat{}, err
	}

	src, err := s.docker.OpenMount(ctx, s.volume.ComposeProject, s.mount.service, s.mount.target)
	if err != nil {
		return model.SnapshotStat{}, fmt.Errorf("open docker mount %s %s: %w", s.mount.service, s.mount.target, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close docker mount")
		}
	}()

	// The counter sits in the stream, so the size reported is the size
	// actually written.
	counted := &countingReader{r: src}
	if err := capture.PutObject(ctx, archiveName, counted); err != nil {
		return model.SnapshotStat{}, fmt.Errorf("write archive: %w", err)
	}
	return model.SnapshotStat{
		Volume:  s.volume,
		Type:    model.SnapshotBackendCopy,
		Payload: Payload{SizeBytes: counted.n},
	}, nil
}

// Restore hands the stored tar stream to WriteMount, which wipes the mount and
// extracts into it. req.Payload is not read: the key is recomputed.
func (s Snapshotter) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	capture, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return err
	}

	src, err := capture.GetObject(ctx, archiveName)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close archive")
		}
	}()

	if err := s.docker.WriteMount(ctx, s.volume.ComposeProject, s.mount.service, s.mount.target, src); err != nil {
		return fmt.Errorf("write docker mount %s %s: %w", s.mount.service, s.mount.target, err)
	}
	return nil
}

func (s Snapshotter) Exists(ctx context.Context, req model.BackendExistsRequest) (bool, error) {
	capture, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return false, err
	}
	exists, err := capture.Exists(ctx, archiveName)
	if err != nil {
		return false, fmt.Errorf("stat archive: %w", err)
	}
	return exists, nil
}

// Remove deletes the archive and stops: a Storager may not delete its own root,
// so the caller sweeps the directory. An archive already gone is not an error.
func (s Snapshotter) Remove(ctx context.Context, req model.BackendRemoveRequest) error {
	capture, err := s.captureStore(req.SnapshotID)
	if err != nil {
		return err
	}
	if err := capture.Delete(ctx, archiveName); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete archive: %w", err)
	}
	return nil
}

// captureStore narrows the tier's root to the one capture a request addresses.
// It is the only place an id becomes a storage key, so the id gate stands here:
// the destructive paths render a key through it too.
func (s Snapshotter) captureStore(snapshotID model.SavepointID) (storages.Storager, error) {
	if err := snapshotID.Validate(); err != nil {
		return nil, err
	}
	capture, err := s.store.SubStorage(path.Join(snapshotID.String(), s.volume.ObjectID), true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at capture %s: %w", snapshotID, err)
	}
	return capture, nil
}

// countingReader tallies the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
