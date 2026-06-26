// Package snapshot defines the shared value types that pair a savepoint's
// volumes with the backend handles to their stored data. The concrete backends
// live in sub-packages (the MVP is repository/snapshot/copy); each service that
// snapshots declares its own backend interface locally and never inspects an
// Opaque handle.
package snapshot

// SnapshotRef is an opaque handle to one volume's stored snapshot. Backend names
// the snapshot backend that produced it; Opaque is interpreted only by that
// backend (a filesystem path for the copy backend, a subvolume id for btrfs, a
// resource id for a cloud backend, ...). Callers outside the creating backend
// never parse Opaque.
type SnapshotRef struct {
	Backend string `json:"backend"`
	Opaque  string `json:"opaque"`
}

// VolumeRef records one snapshotted mount and the backend handle to its data,
// so rollback knows what to clear and which handle to restore. One VolumeRef is
// stored per volume in each manifest savepoint record.
type VolumeRef struct {
	Service   string      `json:"service"`          // compose service that owns the mount
	MountPath string      `json:"mount_path"`       // mount path inside the container
	Type      string      `json:"type"`             // "volume" or "bind"
	Source    string      `json:"source,omitempty"` // Docker volume name or host path; "" if anonymous
	Snapshot  SnapshotRef `json:"snapshot"`         // backend handle to this volume's data
}
