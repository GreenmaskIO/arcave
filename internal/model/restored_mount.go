package model

// RestoredMount is the per-mount entry in the rollback output.
type RestoredMount struct {
	Service   string `json:"service"`
	MountPath string `json:"mount_path"`
}
