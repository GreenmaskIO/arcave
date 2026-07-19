package model

// VolumeRef records one snapshotted mount so rollback knows what to clear and
// restore. One VolumeRef is stored per volume in each savepoint record. The
// archive's storage location is not stored: the snapshot backend recomputes it
// from the savepoint id and these coordinates, so the record stays flat and there
// is no opaque handle to interpret.
type VolumeRef struct {
	Service   string `json:"service"`              // compose service that owns the mount
	MountPath string `json:"mount_path"`           // mount path inside the container
	Type      string `json:"type"`                 // "volume" or "bind"
	Source    string `json:"source,omitempty"`     // Docker volume name or host path; "" if anonymous
	SizeBytes int64  `json:"size_bytes,omitempty"` // bytes stored for this volume's archive
}
