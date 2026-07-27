package model

import "time"

// Savepoint is the metadata of one consistent snapshot of every registered
// stateful volume. The per-volume bookkeeping lives with the SavepointRecord
// that embeds this type. Its json tags are the wire contract: single-savepoint
// command payloads marshal this type directly.
type Savepoint struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
	Stats       []SnapshotStat `json:"stats,omitempty"` // one per snapshotted volume
}
