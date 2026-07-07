// Package backup manages the temporary safety backup GM takes before a
// rollback. It captures the current live state of every mount under
// .gm/tmp/rollback-<id>/ using the same on-disk layout as a savepoint
// (<service>/<nested-mount-path>/data.tar), so a failed rollback can be
// replayed to return the environment to where it started.
//
// Unlike a snapshot it is never recorded in the manifest and carries no
// SnapshotRef: a backup is created, optionally restored, and then deleted. It
// writes through a model.Storager rooted at .gm/tmp and reuses the copy snapshot
// backend's Storager-backed endpoints (snapshot.NewWriter/NewReader) and
// mount-path nesting (snapshot.MountDir), so the two layouts stay identical by
// construction.
package backup

import (
	"context"
	"path"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/snapshot"
	"github.com/greenmaskio/gm-tools/internal/volume"
)

// archiveName is the tar archive holding one mount's data, mirroring the copy
// snapshot backend's layout.
const archiveName = "data.tar"

// baseRel is the project-relative directory the backup Storager is rooted at.
// It is surfaced only in user-facing RelPath messages; the Storager itself
// resolves object keys against it.
var baseRel = filepath.Join(".gm", "tmp")

// Session is a single safety-backup directory, identified by id (typically a
// Unix timestamp). All its archives live under .gm/tmp/rollback-<id>/, written
// through st (whose root is .gm/tmp); rel is that "rollback-<id>" key prefix.
type Session struct {
	st  model.Storager
	rel string
}

// NewSession begins a backup identified by id, writing through st (typically the
// project Storager re-rooted at .gm/tmp via SubStorage). It allocates the key
// prefix but writes nothing — the Storager creates each mount's sub-directory on
// first put.
func NewSession(st model.Storager, id string) *Session {
	return &Session{st: st, rel: "rollback-" + id}
}

// RelPath is the project-relative path of the backup directory. It is surfaced
// in user-facing messages so a retained backup can be located after a failed
// rollback.
func (b *Session) RelPath() string { return filepath.Join(baseRel, b.rel) }

// Writer returns a volume.Writer for one mount's archive plus the object key it
// writes to, which the caller records so the backup can later be read back.
func (b *Session) Writer(service, mountPath string) (volume.Writer, string) {
	key := path.Join(b.rel, service, snapshot.MountDir(mountPath), archiveName)
	return snapshot.NewWriter(b.st, key), key
}

// Reader returns a volume.Reader that streams a stored backup archive back out.
func (b *Session) Reader(key string) volume.Reader {
	return snapshot.NewReader(b.st, key)
}

// Remove deletes the backup directory. It then prunes the shared .gm/tmp parent
// once it is empty — a parent still holding another rollback's backup is left
// untouched. DeleteAll errors on a missing prefix (unlike os.RemoveAll), so the
// session delete is guarded by Exists.
func (b *Session) Remove(ctx context.Context) error {
	exists, err := b.st.Exists(ctx, b.rel)
	if err != nil {
		return err
	}
	if exists {
		if err := b.st.DeleteAll(ctx, b.rel); err != nil {
			return err
		}
	}

	// Prune the shared .gm/tmp parent only when this was the last backup. The
	// Storager is rooted at .gm/tmp, so a ListDir error means it never existed
	// (nothing to prune) and DeleteAll(".") removes the now-empty root itself.
	files, dirs, err := b.st.ListDir(ctx)
	if err != nil {
		return nil
	}
	if len(files) == 0 && len(dirs) == 0 {
		return b.st.DeleteAll(ctx, ".")
	}
	return nil
}
