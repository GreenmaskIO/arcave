package model

// VolumeSnapshotRequest asks a snapshot backend to capture one volume's data. It
// carries the storage namespace to lay the data under and the Docker mount
// coordinates the backend needs to run the whole read→write pipeline itself.
//
// The two "project" concepts are deliberately separate. Namespace is the storage
// tenant the data lands under; ComposeProject is the Docker addressing name the
// backend uses to find the container. The store itself is in neither: it is the
// backend's Storager root, bound at the DI seam, so the request never names a
// directory.
type VolumeSnapshotRequest struct {
	Namespace      Namespace
	ComposeProject string
	Volume         VolumeKey
}
