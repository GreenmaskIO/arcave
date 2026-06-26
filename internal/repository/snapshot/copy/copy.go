// Package copy is the MVP snapshot backend: it stores each volume's data as a
// plain tar archive on the local filesystem, under the configured savepoint
// directory. The archive is written verbatim (never extracted on the host) so
// file ownership and permissions survive a later restore.
package copy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/greenmaskio/gm-tools/internal/repository/snapshot"
)

// Name identifies this backend in a SnapshotRef.
const Name = "copy"

// archiveName is the tar archive holding one volume's data, stored under
// <savepoint-id>/<service-name>/<flattened-mount-path>/.
const archiveName = "data.tar"

// Store places snapshot archives under snapshotsDir. snapshotsDir may be
// relative to projectRoot or absolute; relative paths are resolved against
// projectRoot so a SnapshotRef stays valid across runs from the project root.
type Store struct {
	projectRoot  string
	snapshotsDir string
}

// New returns a copy-backend store rooted at projectRoot writing under
// snapshotsDir.
func New(projectRoot, snapshotsDir string) *Store {
	return &Store{projectRoot: projectRoot, snapshotsDir: snapshotsDir}
}

// MountDir returns the snapshot sub-directory for a mount: the container mount
// path nested verbatim (leading "/" stripped), e.g. "/export/data" ->
// "export/data". Always combine it with filepath.Join, which renders the
// embedded slashes as nested directories. Nesting the real path — rather than
// flattening "/" to "_" — keeps the layout collision-free, since the filesystem
// distinguishes "/export/data" from "/export_data" natively.
func MountDir(mountPath string) string {
	return strings.TrimPrefix(mountPath, "/")
}

// Dest creates the directory for a volume's archive and returns the absolute
// archive path to write, plus the SnapshotRef to persist in the manifest.
func (s *Store) Dest(savepointID, service, mountPath string) (absPath string, ref snapshot.SnapshotRef, err error) {
	rel := filepath.Join(s.snapshotsDir, savepointID, service, MountDir(mountPath), archiveName)
	absPath = s.abs(rel)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return "", snapshot.SnapshotRef{}, fmt.Errorf("create snapshot directory: %w", err)
	}
	return absPath, snapshot.SnapshotRef{Backend: Name, Opaque: rel}, nil
}

// Resolve returns the absolute archive path for a stored ref.
func (s *Store) Resolve(ref snapshot.SnapshotRef) string {
	return s.abs(ref.Opaque)
}

// Remove deletes every archive of a savepoint.
func (s *Store) Remove(savepointID string) error {
	return os.RemoveAll(s.abs(filepath.Join(s.snapshotsDir, savepointID)))
}

func (s *Store) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.projectRoot, p)
}
