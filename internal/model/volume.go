package model

// SnapshotRef is an opaque handle to one volume's stored snapshot. Backend names
// the snapshot backend that produced it; Opaque is interpreted only by that
// backend (a filesystem path for the copy backend, a subvolume id for btrfs, a
// resource id for a cloud backend, ...). Callers outside the creating backend
// never parse Opaque.
type SnapshotRef struct {
	Backend string `json:"backend"`
	Opaque  string `json:"opaque"`
}

// VolumeSnapshotMetadata is what a snapshot backend returns after storing one
// volume's data: the handle to fetch it back (the embedded SnapshotRef) plus
// stats about what was actually stored. The copy backend reports the raw archive
// size; a dedup/content-addressed backend reports the size *after* dedup and may
// grow the struct over time (Checksum, Chunks, DedupSavedBytes, ...). Embedding
// SnapshotRef keeps the manifest shape flat: {"backend":..,"opaque":..,"size_bytes":..}.
// It is recorded in the manifest inside each VolumeRef.
type VolumeSnapshotMetadata struct {
	SnapshotRef
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

// VolumeRef records one snapshotted mount and the backend handle to its data,
// so rollback knows what to clear and which handle to restore. One VolumeRef is
// stored per volume in each savepoint record.
type VolumeRef struct {
	Service   string                 `json:"service"`          // compose service that owns the mount
	MountPath string                 `json:"mount_path"`       // mount path inside the container
	Type      string                 `json:"type"`             // "volume" or "bind"
	Source    string                 `json:"source,omitempty"` // Docker volume name or host path; "" if anonymous
	Snapshot  VolumeSnapshotMetadata `json:"snapshot"`         // backend handle + stored-size metadata
}
