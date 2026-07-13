package model

// SavepointRecord is a stored savepoint: its metadata together with one
// VolumeRef per snapshotted mount. It is the unit the manifest repository
// persists and returns. The embedded Savepoint metadata alone is the command
// wire contract; Volumes carry the per-mount backend handles rollback needs.
type SavepointRecord struct {
	Savepoint
	Volumes []VolumeRef `json:"volumes"`
}
