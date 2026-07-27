package snapshot

import "path"

// archiveName is the object every volume's tar stream is stored as, at the leaf
// of its snapshotKey. One archive per volume, so the name never varies.
const archiveName = "data.tar"

// snapshotKey returns the storage key of one volume's archive within a savepoint:
//
//	100/volume/demo_pgdata/data.tar        a named volume, by docker name
//	100/service/db/export/data/data.tar    a bind or anon mount, by service and
//	                                       container path
//
// The layout falls out of the volume's ObjectID rather than being invented here,
// so it needs no branch on type: model's "volume/" and "service/" prefixes keep
// the identity spaces apart, and a mount path is nested verbatim minus its
// leading "/" — collision-free the way flattening "/" to "_" would not be. Every
// key is therefore ≥2 segments deep and can never be the reserved savepoint.json.
//
// This package owns the key because it is the only one that renders it: the write
// path to store an archive, the read path to find one, both on CopyBackend. So a
// savepoint id and its volumes fully describe a restore, with no handle to record.
//
// The key is always slash-separated: it addresses an object in a Storager, and
// only the directory backend turns it into a host path. It validates nothing —
// the gates stand earlier (model.ValidSavepointID on the write and destructive
// paths, the library's Guard below).
func snapshotKey(savepointID, objectID string) string {
	return path.Join(savepointID, objectID, archiveName)
}
