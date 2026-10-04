// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

// BaseImageIDLabel names, on an image arcave committed, the id of the image the user's
// compose file named: after a rollback the container runs on an arcave/…:sp-<id> tag, and
// this label is what still points past it to the true base.
const BaseImageIDLabel = "io.arcave.base-image-id"

// ContainerRef names one replica of one service. Replica is
// com.docker.compose.container-number, counted from 1.
type ContainerRef struct {
	Service   string
	Replica   int
	ID        string
	BaseImage string // what the container was created from; an arcave/…:sp-<id> tag after a rollback
}

// ImageCaptureRequest is one commit-and-store, addressed by savepoint and container.
type ImageCaptureRequest struct {
	SnapshotID SavepointID
	Container  ContainerRef
}

// ImageCapture is one container's writable layer, committed and stored. It
// travels in the savepoint record, so its json tags are the wire contract.
type ImageCapture struct {
	Service     string        `json:"service"`
	Replica     int           `json:"replica"`
	ContainerID string        `json:"container_id"`
	ImageID     ImageDigestID `json:"image_id"`
	BaseImage   string        `json:"base_image,omitempty"`
	SizeBytes   int64         `json:"size_bytes"`
	// StoredBytes is what this capture added to the store; below SizeBytes when
	// deduplication found the bytes already there.
	StoredBytes int64 `json:"stored_bytes,omitempty"`
}

// ImageRestoreRequest is one load-and-tag, addressed by the savepoint that holds
// the archive and the capture it recorded. ImageRef is the docker reference the
// restored image is to answer to in the local daemon.
type ImageRestoreRequest struct {
	SnapshotID SavepointID
	Capture    ImageCapture
	ImageRef   string
}

// ImageRestore is one writable layer put back into the daemon: ImageID digests its
// content, ImageRef is the docker reference the rebuilt stack names it by. It
// travels in the rollback response, so its json tags are the wire contract.
type ImageRestore struct {
	Service  string        `json:"service"`
	Replica  int           `json:"replica"`
	ImageID  ImageDigestID `json:"image_id"`
	ImageRef string        `json:"image_ref"`
}
