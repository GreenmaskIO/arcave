// Package snapshot is the snapshot backend: it stores and restores a whole
// savepoint's volume data as a unit. Each volume is stored as a plain tar archive
// through a storages.Storager (the local-directory backend today). The archive is
// written verbatim (never extracted on the host) so file ownership and
// permissions survive a later restore.
//
// CopyBackend runs the whole cycle itself, including the per-volume fan-out. It
// holds a DockerVolume (the mount endpoints) and the Storager of the store it
// writes into, so given a savepoint id and its volumes it opens each source,
// writes storage, and on restore reads storage and writes each mount — the
// service never wires the two halves or bounds the concurrency. The store is
// bound at construction, not named per call: a savepoint store and a
// safety-backup store are two backend instances over two stores.
//
// Two things are this package's own policy, and the service names neither:
//
//   - The on-disk layout: snapshotKey(savepointID, volume.ObjectID)
//     (snapshot_key.go), rendered identically on both sides, so there is no
//     handle to record.
//   - The way in: this backend moves bytes through a container that mounts the
//     volume, so it picks that mount itself (accessPath, access_path.go). A
//     backend reading a volume directly would make no such choice — which is why
//     the choice is not in the seam.
package snapshot

import (
	"context"
	"fmt"
	"io"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// maxConcurrentVolumeOperations caps how many volume export/restore operations
// run in parallel within one savepoint, bounding pressure on Docker and the host
// filesystem.
const maxConcurrentVolumeOperations = 5

// FullCopyStat is this backend's SnapshotStat payload. It is owned here, beside
// the code that mints it, for the same reason the storage key is: a payload means
// whatever its backend says it means.
type FullCopyStat struct {
	SizeBytes int64 `json:"size_bytes"`
}

// DockerVolume is what the backend needs from the Docker engine: stream a
// mount's contents out (OpenMount, the export side) and wipe+extract a stream
// back in (WriteMount, the restore side). The concrete *docker.Engine satisfies
// it. Close is deliberately absent — the engine's lifecycle belongs to whoever
// created it (the DI root), not to the backend it is injected into.
//
// It speaks (project, service, target) strings and stayed that way through the
// move to model.Volume above it: that is Docker's addressing, not a domain model.
type DockerVolume interface {
	OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error)
	WriteMount(ctx context.Context, project, service, target string, r io.Reader) error
}

// CopyBackend stores a savepoint's tar streams verbatim through its Storager and
// streams them back on restore. It owns the whole transfer end to end: fan out
// over the volumes, open each Docker mount, write storage, and the mirror on
// restore.
type CopyBackend struct {
	docker DockerVolume
	// storage is rooted at the store this backend writes into — the savepoint
	// directory or the safety-backup directory. One instance serves one store.
	storage storages.Storager
}

// NewCopyBackend wires a copy backend to a ready Docker engine and the Storager
// of the store it owns, already re-rooted at that store's directory. Both are
// shared, concurrency-safe dependencies from the DI root; the backend owns
// neither's lifecycle.
func NewCopyBackend(docker DockerVolume, storage storages.Storager) *CopyBackend {
	return &CopyBackend{docker: docker, storage: storage}
}

// Snapshot captures every volume into the savepoint's directory concurrently,
// returning one stat per volume in the same order as volumes. The savepoint id is
// validated before any key is rendered, since it is the write path. On the first
// volume failure the group is cancelled and the error returned; the caller drops
// the whole directory (Remove) to clean up the partial fan-out.
func (b *CopyBackend) Snapshot(
	ctx context.Context, savepointID, composeProject string, volumes []model.Volume,
) ([]model.SnapshotStat, error) {
	if err := model.ValidSavepointID(savepointID); err != nil {
		return nil, err
	}

	// Each stat lands at its own index, so the goroutines never race on the slice
	// and the caller keeps volume order.
	stats := make([]model.SnapshotStat, len(volumes))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for i, v := range volumes {
		g.Go(func() error {
			zerolog.Ctx(gctx).Info().Str("volume", v.ObjectID).Msg("snapshotting volume")
			size, err := b.snapshotVolume(gctx, savepointID, composeProject, v)
			if err != nil {
				return fmt.Errorf("snapshot %s: %w", v.ObjectID, err)
			}
			zerolog.Ctx(gctx).Info().Str("volume", v.ObjectID).Int64("size_bytes", size).Msg("volume snapshotted")
			stats[i] = model.SnapshotStat{
				Volume:  v,
				Type:    model.SnapshotBackendFullCopy,
				Payload: FullCopyStat{SizeBytes: size},
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return stats, nil
}

// snapshotVolume streams one volume out through the mount accessPath picked and
// stores it verbatim under snapshotKey. The Docker mount is the only closer, so it
// is always closed; a close error is logged, not returned — the bytes are stored.
func (b *CopyBackend) snapshotVolume(
	ctx context.Context, savepointID, composeProject string, v model.Volume,
) (int64, error) {
	service, target, err := accessPath(v)
	if err != nil {
		return 0, err
	}

	src, err := b.docker.OpenMount(ctx, composeProject, service, target)
	if err != nil {
		return 0, fmt.Errorf("open docker mount %s %s: %w", service, target, err)
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
	if err := b.storage.PutObject(ctx, snapshotKey(savepointID, v.ObjectID), counted); err != nil {
		return 0, fmt.Errorf("write storage: %w", err)
	}
	return counted.n, nil
}

// Restore streams every stored volume back into the project concurrently, the
// mirror of Snapshot. A restore failure fails the whole call; the caller reverts.
func (b *CopyBackend) Restore(ctx context.Context, savepointID, composeProject string, volumes []model.Volume) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for _, v := range volumes {
		g.Go(func() error {
			zerolog.Ctx(gctx).Info().Str("volume", v.ObjectID).Msg("restoring volume")
			if err := b.restoreVolume(gctx, savepointID, composeProject, v); err != nil {
				return fmt.Errorf("restore %s: %w", v.ObjectID, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// restoreVolume recomputes the storage key from the savepoint id and the volume's
// object id, opens the stored object, and hands the tar stream to WriteMount,
// which wipes the mount and extracts it. The storage object is the only closer, so
// it is always closed; a close error is logged, not returned — the bytes are read.
func (b *CopyBackend) restoreVolume(ctx context.Context, savepointID, composeProject string, v model.Volume) error {
	service, target, err := accessPath(v)
	if err != nil {
		return err
	}

	src, err := b.storage.GetObject(ctx, snapshotKey(savepointID, v.ObjectID))
	if err != nil {
		return fmt.Errorf("open storage object: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("close storage object")
		}
	}()

	if err := b.docker.WriteMount(ctx, composeProject, service, target, src); err != nil {
		return fmt.Errorf("write docker mount %s %s: %w", service, target, err)
	}
	return nil
}

// Missing reports which of the savepoint's volumes have no archive in the store,
// so a pre-flight can name every gap at once rather than abort on the first.
func (b *CopyBackend) Missing(ctx context.Context, savepointID string, volumes []model.Volume) ([]model.Volume, error) {
	var missing []model.Volume
	for _, v := range volumes {
		exists, err := b.storage.Exists(ctx, snapshotKey(savepointID, v.ObjectID))
		if err != nil {
			return nil, fmt.Errorf("check archive for %s: %w", v.ObjectID, err)
		}
		if !exists {
			missing = append(missing, v)
		}
	}
	return missing, nil
}

// Remove deletes every object under one savepoint's directory — a savepoint's, or
// a safety backup's. It guards on Exists first because Storager.DeleteAll errors
// on a missing prefix (unlike os.RemoveAll).
//
// Deleting the whole directory, rather than a set of archives, is what lets the
// service drop a *partial* snapshot: when a volume fails mid-fan-out there are
// only half-written archives on disk. It is why Remove is savepoint-scoped while
// the transfer methods are per-volume underneath.
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

// countingReader tallies the bytes read through it, so snapshotVolume can report
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
