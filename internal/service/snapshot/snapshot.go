// Package snapshot is the snapshot backend: it stores and restores a whole
// savepoint's volume data as a unit. Each volume is stored as a plain tar archive
// through a model.Storager (the local-directory backend today). The archive is
// written verbatim (never extracted on the host) so file ownership and
// permissions survive a later restore.
//
// CopyBackend runs the whole cycle itself, including the per-volume fan-out. It
// holds a DockerVolume (the mount endpoints) and the Storager of the store it
// writes into, so given a savepoint id and its mounts it opens each source,
// writes storage, and on restore reads storage and writes each mount — the
// service never wires the two halves or bounds the concurrency. The store is
// bound at construction, not named per call: a savepoint store and a
// safety-backup store are two backend instances over two stores.
//
// The on-disk layout is this package's own: each archive's key is
// snapshotKey(savepointID, service, mountPath) (snapshot_key.go), computed
// identically on the write and read sides, so there is no opaque handle to
// record — a restore is fully described by the savepoint id and the mounts.
package snapshot

import (
	"context"
	"fmt"
	"io"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// maxConcurrentVolumeOperations caps how many mount export/restore operations run
// in parallel within one savepoint, bounding pressure on Docker and the host
// filesystem.
const maxConcurrentVolumeOperations = 5

// DockerVolume is what the backend needs from the Docker engine: stream a
// mount's contents out (OpenMount, the export side) and wipe+extract a stream
// back in (WriteMount, the restore side). The concrete *docker.Engine satisfies
// it. Close is deliberately absent — the engine's lifecycle belongs to whoever
// created it (the DI root), not to the backend it is injected into.
type DockerVolume interface {
	OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error)
	WriteMount(ctx context.Context, project, service, target string, r io.Reader) error
}

// CopyBackend stores a savepoint's tar streams verbatim through its Storager and
// streams them back on restore. It owns the whole transfer end to end: fan out
// over the mounts, open each Docker mount, write storage, and the mirror on
// restore.
type CopyBackend struct {
	docker DockerVolume
	// storage is rooted at the store this backend writes into — the savepoint
	// directory or the safety-backup directory. One instance serves one store.
	storage model.Storager
}

// NewCopyBackend wires a copy backend to a ready Docker engine and the Storager
// of the store it owns, already re-rooted at that store's directory. Both are
// shared, concurrency-safe dependencies from the DI root; the backend owns
// neither's lifecycle.
func NewCopyBackend(docker DockerVolume, storage model.Storager) *CopyBackend {
	return &CopyBackend{docker: docker, storage: storage}
}

// Snapshot captures every mount into the savepoint's directory concurrently,
// returning the bytes stored per mount in the same order as mounts. The savepoint
// id is validated before any key is rendered, since it is the write path. On the
// first mount failure the group is cancelled and the error returned; the caller
// drops the whole directory (Remove) to clean up the partial fan-out.
func (b *CopyBackend) Snapshot(ctx context.Context, savepointID, composeProject string, mounts []model.Mount) ([]int64, error) {
	if err := model.ValidSavepointID(savepointID); err != nil {
		return nil, err
	}

	// Each size lands at its own index, so the goroutines never race on the slice
	// and the caller keeps mount order.
	sizes := make([]int64, len(mounts))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for i, m := range mounts {
		g.Go(func() error {
			zerolog.Ctx(gctx).Info().Str("service", m.Service).Str("mount", m.Target).Msg("snapshotting mount")
			size, err := b.snapshotMount(gctx, savepointID, composeProject, m)
			if err != nil {
				return fmt.Errorf("snapshot %s %s: %w", m.Service, m.Target, err)
			}
			zerolog.Ctx(gctx).Info().Str("service", m.Service).Str("mount", m.Target).Msg("mount snapshotted")
			sizes[i] = size
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return sizes, nil
}

// snapshotMount streams one volume out of its Docker mount and stores it verbatim
// under snapshotKey. The Docker mount is the only closer, so it is always
// closed; a close error is logged, not returned, since the bytes are already
// stored.
func (b *CopyBackend) snapshotMount(ctx context.Context, savepointID, composeProject string, m model.Mount) (int64, error) {
	src, err := b.docker.OpenMount(ctx, composeProject, m.Service, m.Target)
	if err != nil {
		return 0, fmt.Errorf("open docker mount: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close docker mount")
		}
	}()

	// PutObject pulls from src at storage speed — intrinsic backpressure, so no
	// io.Pipe is needed between the mount and the store. The counter sits in the
	// stream rather than stat'ing the object afterwards, so the size reported is
	// the size actually written.
	counted := &countingReader{r: src}
	if err := b.storage.PutObject(ctx, snapshotKey(savepointID, m.Service, m.Target), counted); err != nil {
		return 0, fmt.Errorf("write storage: %w", err)
	}
	return counted.n, nil
}

// Restore streams every stored mount back into its Docker mount concurrently, the
// mirror of Snapshot. A restore failure fails the whole call; the caller reverts.
func (b *CopyBackend) Restore(ctx context.Context, savepointID, composeProject string, mounts []model.Mount) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for _, m := range mounts {
		g.Go(func() error {
			zerolog.Ctx(gctx).Info().Str("service", m.Service).Str("mount", m.Target).Msg("restoring mount")
			if err := b.restoreMount(gctx, savepointID, composeProject, m); err != nil {
				return fmt.Errorf("restore %s %s: %w", m.Service, m.Target, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// restoreMount recomputes the storage key from the savepoint id and the mount,
// opens the stored object, and hands the tar stream to WriteMount, which wipes
// the mount and extracts it. The storage object is the only closer, so it is
// always closed; a close error is logged, not returned, since the bytes are
// already consumed.
func (b *CopyBackend) restoreMount(ctx context.Context, savepointID, composeProject string, m model.Mount) error {
	src, err := b.storage.GetObject(ctx, snapshotKey(savepointID, m.Service, m.Target))
	if err != nil {
		return fmt.Errorf("open storage object: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close storage object")
		}
	}()

	if err := b.docker.WriteMount(ctx, composeProject, m.Service, m.Target, src); err != nil {
		return fmt.Errorf("write docker mount: %w", err)
	}
	return nil
}

// Missing reports which of the savepoint's mounts have no archive in the store,
// so a pre-flight can name every gap at once rather than abort on the first.
func (b *CopyBackend) Missing(ctx context.Context, savepointID string, mounts []model.Mount) ([]model.Mount, error) {
	var missing []model.Mount
	for _, m := range mounts {
		exists, err := b.storage.Exists(ctx, snapshotKey(savepointID, m.Service, m.Target))
		if err != nil {
			return nil, fmt.Errorf("check archive for %s %s: %w", m.Service, m.Target, err)
		}
		if !exists {
			missing = append(missing, m)
		}
	}
	return missing, nil
}

// Remove deletes every object under one savepoint's directory — a savepoint's, or
// a safety backup's. It guards on Exists first because Storager.DeleteAll errors
// on a missing prefix (unlike os.RemoveAll).
//
// Deleting the whole directory, rather than a set of archives, is what lets the
// service drop a *partial* snapshot: when a mount fails mid-fan-out there are only
// half-written archives on disk. It is why Remove is savepoint-scoped while the
// transfer methods are per-mount underneath.
//
// The savepoint id is validated before it is rendered into a storage key: this is
// the destructive call, so an id that is not a Unix timestamp must not reach the
// Storager at all.
func (b *CopyBackend) Remove(ctx context.Context, savepointID string) error {
	if err := model.ValidSavepointID(savepointID); err != nil {
		return err
	}
	exists, err := b.storage.Exists(ctx, savepointID)
	if err != nil {
		return fmt.Errorf("check key '%s' exists: %w", savepointID, err)
	}
	if !exists {
		return nil
	}
	return b.storage.DeleteAll(ctx, savepointID)
}

// countingReader tallies the bytes read through it, so snapshotMount can report
// how much it actually stored without buffering or re-reading the stream.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
