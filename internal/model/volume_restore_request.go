package model

// VolumeRestoreRequest asks a snapshot backend to stream one stored volume back
// into its Docker mount. Snapshot is the handle Create returned, which the
// backend interprets to locate the stored data — it already locates it, so no
// Namespace is needed. ComposeProject + Volume name the live mount to write into.
type VolumeRestoreRequest struct {
	ComposeProject string
	Volume         VolumeKey
	Snapshot       SnapshotRef
}
