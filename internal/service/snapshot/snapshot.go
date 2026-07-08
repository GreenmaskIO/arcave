// Package snapshot is the MVP snapshot backend ("copy"): it stores each volume's
// data as a plain tar archive through a model.Storager (the local-directory
// backend today). The archive is written verbatim (never extracted on the host)
// so file ownership and permissions survive a later restore.
//
// CopyBackend runs the whole cycle itself. It holds a DockerVolume (the mount
// endpoints) and the Storager of the store it writes into — so the backend opens
// the source, writes storage, and on restore reads storage and writes the mount,
// with the service never wiring the two halves. The store is bound at
// construction, not named per request: a savepoint store and a safety-backup
// store are two backend instances, and nothing outside the backend knows a
// snapshot is a path. This is the seam a future dedup/content-addressed backend
// replaces: such a backend chunks the source mid-stream and persists only the
// blocks it does not already hold. Create returns the metadata to record in the
// manifest (a model.SnapshotRef plus the stored size), computed *after* the
// write, so a content-addressed handle is derivable from the data.
package snapshot

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// BackendNameFullCopy identifies this backend in a SnapshotRef.
const BackendNameFullCopy = "FullCopy"

// archiveName is the tar archive holding one volume's data, stored under
// <namespace>/<service-name>/<nested-mount-path>/.
const archiveName = "data.tar"

// DockerVolume is what the backend needs from the Docker engine: stream a
// mount's contents out (OpenMount, the export side) and wipe+extract a stream
// back in (WriteMount, the restore side). The concrete *docker.Engine satisfies
// it. Close is deliberately absent — the engine's lifecycle belongs to whoever
// created it (the DI root), not to the backend it is injected into.
type DockerVolume interface {
	OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error)
	WriteMount(ctx context.Context, project, service, target string, r io.Reader) error
}

// CopyBackend is the MVP "copy" snapshot backend. It stores a volume's tar
// stream verbatim through its Storager and streams it back on restore. It owns
// the whole transfer, so a dedup/CAS backend drops in behind the same interface,
// cutting the source stream on the fly and storing only new blocks.
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

// Create streams the volume out of its Docker mount and stores it verbatim
// through the backend's Storager, returning the metadata to persist in the
// manifest. The copy backend streams the source straight into a single
// PutObject; a dedup backend would chunk the source here and do one PutObject
// per *new* chunk. The ref is built from the write result, not before it, so a
// content-addressed handle is a drop-in. The Docker mount is the only closer, so
// Create always closes it; a close error is logged, not returned, since the
// bytes are already stored.
func (b *CopyBackend) Create(ctx context.Context, req model.VolumeSnapshotRequest) (model.VolumeSnapshotMetadata, error) {
	if err := req.Namespace.Validate(); err != nil {
		return model.VolumeSnapshotMetadata{}, err
	}
	name := objectName(req.Namespace, req.Volume)

	src, err := b.docker.OpenMount(ctx, req.ComposeProject, req.Volume.Service, req.Volume.MountPath)
	if err != nil {
		return model.VolumeSnapshotMetadata{}, fmt.Errorf("open docker mount: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close docker mount")
		}
	}()

	// PutObject pulls from src at storage speed — intrinsic backpressure, so no
	// io.Pipe is needed between the mount and the store. The counter sits in the
	// stream rather than stat'ing the object afterwards, so the size reported is
	// the size actually written.
	counted := &countingReader{r: src}
	if err := b.storage.PutObject(ctx, name, counted); err != nil {
		return model.VolumeSnapshotMetadata{}, fmt.Errorf("write storage: %w", err)
	}

	return model.VolumeSnapshotMetadata{
		SnapshotRef: model.SnapshotRef{
			Backend: BackendNameFullCopy,
			Opaque:  name,
		},
		SizeBytes: counted.n,
	}, nil
}

// Restore streams the snapshot named by req.Snapshot back out of storage and
// into its Docker mount. It is the mirror of Create: the backend opens the
// stored object (one GetObject for the copy backend, many for a dedup one) and
// hands the reassembled tar stream to WriteMount, which wipes the mount and
// extracts it. The storage object is the only closer, so Restore always closes
// it; a close error is logged, not returned, since the bytes are already
// consumed.
func (b *CopyBackend) Restore(ctx context.Context, req model.VolumeRestoreRequest) error {
	if err := checkBackend(req.Snapshot); err != nil {
		return err
	}

	src, err := b.storage.GetObject(ctx, req.Snapshot.Opaque)
	if err != nil {
		return fmt.Errorf("open storage object: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("close storage object")
		}
	}()

	if err := b.docker.WriteMount(ctx, req.ComposeProject, req.Volume.Service, req.Volume.MountPath, src); err != nil {
		return fmt.Errorf("write docker mount: %w", err)
	}
	return nil
}

// Exists reports whether the archive for ref is present in this backend's store.
func (b *CopyBackend) Exists(ctx context.Context, ref model.SnapshotRef) (bool, error) {
	if err := checkBackend(ref); err != nil {
		return false, err
	}
	return b.storage.Exists(ctx, ref.Opaque)
}

// Remove deletes every object under one namespace — a savepoint's, or a safety
// backup's. It guards on Exists first because Storager.DeleteAll errors on a
// missing prefix (unlike os.RemoveAll).
//
// Deleting a whole namespace, rather than a set of refs, is what lets the
// service drop a *partial* snapshot: when a mount fails mid-fan-out there are
// no refs yet, only half-written archives on disk. A dedup backend sharing
// chunks across savepoints implements this through the per-namespace index it
// needs for GC anyway.
//
// The namespace is validated before it is rendered into a storage key: this is
// the destructive call, so an id that is not a Unix timestamp must not reach the
// Storager at all.
func (b *CopyBackend) Remove(ctx context.Context, ns model.Namespace) error {
	if err := ns.Validate(); err != nil {
		return err
	}
	key := ns.String()
	exists, err := b.storage.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("check key '%s' exists: %w", key, err)
	}
	if !exists {
		return nil
	}
	return b.storage.DeleteAll(ctx, key)
}

// checkBackend rejects a ref minted by another backend, whose Opaque this one
// cannot interpret. Without it a manifest written by backend A would be silently
// restored by backend B.
func checkBackend(ref model.SnapshotRef) error {
	if ref.Backend != BackendNameFullCopy {
		return fmt.Errorf("snapshot was created by backend %q, not %q", ref.Backend, BackendNameFullCopy)
	}
	return nil
}

// countingReader tallies the bytes read through it, so Create can report how
// much it actually stored without buffering or re-reading the stream.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// MountDir returns the snapshot sub-directory for a mount: the container mount
// path nested verbatim (leading "/" stripped), e.g. "/export/data" ->
// "export/data". Always combine it with path.Join, which renders the embedded
// slashes as nested directories. Nesting the real path — rather than flattening
// "/" to "_" — keeps the layout collision-free, since the filesystem
// distinguishes "/export/data" from "/export_data" natively.
func MountDir(mountPath string) string {
	return strings.TrimPrefix(mountPath, "/")
}

// objectName builds the archive key for one mount, relative to this backend's
// Storager root: "<namespace>/<service>/<nested-mount-path>/data.tar". This is
// the single place that owns the on-disk layout.
//
// The namespace directory belongs to the savepoint layer, which reserves
// "<namespace>/savepoint.json" for its record. Every key minted here is at least
// two segments deep, so a backend archive can never collide with it.
func objectName(ns model.Namespace, vol model.VolumeKey) string {
	return path.Join(ns.String(), vol.Service, MountDir(vol.MountPath), archiveName)
}
