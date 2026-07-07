// Package snapshot is the MVP snapshot backend: it stores each volume's data as
// a plain tar archive through a model.Storager (the local-directory backend
// today), under the configured savepoint directory. The archive is written
// verbatim (never extracted on the host) so file ownership and permissions
// survive a later restore.
//
// The backend never touches the filesystem itself; it mints volume.Reader/Writer
// endpoints (see reader.go / writer.go) backed by the injected Storager and lets
// internal/volume.Copy move the bytes. NewWriter/NewReader are exported so the
// safety-backup store, which shares this layout, reuses the same Storager-backed
// endpoints.
package snapshot

import (
	"context"
	"path"
	"strings"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/volume"
)

// Name identifies this backend in a SnapshotRef.
const Name = "copy"

// archiveName is the tar archive holding one volume's data, stored under
// <savepoint-id>/<service-name>/<nested-mount-path>/.
const archiveName = "data.tar"

// Snapshot places snapshot archives through st, whose root (cwd) is the savepoint
// directory. Object keys, and hence SnapshotRef.Opaque, are relative to that
// root: "<savepoint-id>/<service>/<nested-mount-path>/data.tar".
type Snapshot struct {
	st model.Storager
}

// New returns a snapshot backend writing through st (typically the project
// Storager re-rooted at the savepoint directory via SubStorage).
func New(st model.Storager) *Snapshot {
	return &Snapshot{st: st}
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

// Writer returns a volume.Writer that stores a volume's archive, plus the
// SnapshotRef to persist in the manifest. Parent directories are created lazily
// by the Storager on first write.
func (s *Snapshot) Writer(savepointID, service, mountPath string) (volume.Writer, model.SnapshotRef) {
	objectPath := path.Join(savepointID, service, MountDir(mountPath), archiveName)
	return NewWriter(s.st, objectPath), model.SnapshotRef{Backend: Name, Opaque: objectPath}
}

// Reader returns a volume.Reader that streams a stored snapshot back out.
func (s *Snapshot) Reader(ref model.SnapshotRef) volume.Reader {
	return NewReader(s.st, ref.Opaque)
}

// Exists reports whether the archive for ref is present.
func (s *Snapshot) Exists(ctx context.Context, ref model.SnapshotRef) (bool, error) {
	return s.st.Exists(ctx, ref.Opaque)
}

// Remove deletes every archive of a savepoint. It guards on Exists first because
// Storager.DeleteAll errors on a missing prefix (unlike os.RemoveAll).
func (s *Snapshot) Remove(ctx context.Context, savepointID string) error {
	exists, err := s.st.Exists(ctx, savepointID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return s.st.DeleteAll(ctx, savepointID)
}
