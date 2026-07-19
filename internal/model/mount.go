package model

// Mount is one persistent mount of a service that arcave snapshots, keyed by the
// service and the path it is mounted at inside the container — the stable key
// that works for named volumes, bind mounts, and anonymous volumes alike.
type Mount struct {
	Service string // compose service that owns the mount
	Target  string // mount path inside the container
	Type    string // "volume" or "bind"
	// Source identifies the underlying storage to clear on restore: a resolved
	// Docker volume name, a host path for bind mounts, or "" for an anonymous
	// volume (whose hashed name is resolved from the container at restore time).
	Source string
}
