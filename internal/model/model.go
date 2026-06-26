// Package model holds GM's shared domain types, passed between the repository,
// client, service, and adapter layers. It is the base of the dependency graph
// and imports no other internal package.
package model

import "time"

// Mount is one persistent mount of a service that GM snapshots, keyed by the
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

// SkippedMount is a mount GM cannot snapshot (tmpfs and other non-persistent
// mount types), reported so callers can warn about state that is not captured.
type SkippedMount struct {
	Service string
	Reason  string
}

// Savepoint is the metadata of one consistent snapshot of every registered
// stateful volume. The per-volume bookkeeping lives with the manifest record
// that embeds this type.
type Savepoint struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}
