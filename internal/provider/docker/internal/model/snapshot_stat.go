package model

// SnapshotStat is the result of snapshotting one volume. Payload is the
// backend's own: full-copy — bytes stored, cdc — stream position.
//
// It is also the savepoint's per-volume bookkeeping: a restore addresses each
// archive from the Volume here. Payload decodes back as a map[string]any, which
// costs nothing while no reader outside its own backend interprets one.
type SnapshotStat struct {
	Volume  Volume              `json:"volume"`
	Type    SnapshotBackendKind `json:"type"`
	Payload any                 `json:"payload,omitempty"`
}
