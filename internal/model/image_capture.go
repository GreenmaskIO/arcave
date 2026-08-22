package model

// ContainerRef names one replica of one service. Replica is
// com.docker.compose.container-number, counted from 1.
type ContainerRef struct {
	Service   string
	Replica   int
	ID        string
	BaseImage string // what the container was created from
}

// ImageCaptureRequest is one commit-and-store, addressed by savepoint and container.
type ImageCaptureRequest struct {
	SnapshotID SavepointID
	Container  ContainerRef
}

// ImageCapture is one container's writable layer, committed and stored. It
// travels in the savepoint record, so its json tags are the wire contract.
type ImageCapture struct {
	Service     string `json:"service"`
	Replica     int    `json:"replica"`
	ContainerID string `json:"container_id"`
	ImageID     string `json:"image_id"`
	BaseImage   string `json:"base_image,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
}
