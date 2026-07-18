package snapshot

import (
	"path"
	"strings"
)

// archiveName is the object every mount's tar stream is stored as, at the leaf
// of its snapshotKey. One archive per mount, so the name never varies.
const archiveName = "data.tar"

// snapshotKey returns the storage key of one mount's archive within a savepoint:
//
//	<savepointID>/<service>/<mount-path>/data.tar
//
// The mount path is nested verbatim with its leading "/" stripped, so a mount at
// /export/data lands under "export/data/". Nesting the real path keeps the layout
// collision-free — the store distinguishes /export/data from /export_data, which
// flattening "/" to "_" would not.
//
// This package owns the key because it is the only one that renders it: the
// write path renders it to store an archive, the read path renders it again to
// find one, and both are methods on CopyBackend. Nothing is recorded to address
// an archive — a savepoint id and its mounts fully describe a restore, so there
// is no opaque handle in the savepoint record for a reader to interpret.
//
// The key is always slash-separated: it addresses an object in a Storager, not a
// file on the host, and only the directory backend turns it into a host path.
// snapshotKey does not validate its arguments — path safety is the caller's
// gate, and it stands earlier: model.ValidSavepointID admits digits alone on the
// write and destructive paths, and the directory backend's own resolve refuses
// any key that would escape the store.
func snapshotKey(savepointID, service, mountPath string) string {
	return path.Join(savepointID, service, strings.TrimPrefix(mountPath, "/"), archiveName)
}
