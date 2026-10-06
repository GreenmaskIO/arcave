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

// BaseImageRefLabel names, beside BaseImageIDLabel, the reference the user's compose
// file named that image by.
const BaseImageRefLabel = "io.arcave.base-image-ref"

// ContainerRef names one replica of one service. Replica is
// com.docker.compose.container-number, counted from 1.
type ContainerRef struct {
	Service string // compose service name
	Replica int    // replica number, from 1
	ID      string // container id
	Name    string // container name, without the leading slash
	// The upstream: the image the compose file named; empty when it can no longer
	// be traced.
	UpstreamImageID  ImageDigestID // sha256 digest of the upstream's config
	UpstreamImageRef string        // the upstream's name, e.g. postgres:16; never a digest
}

// ImageCaptureRequest is one commit-and-store, addressed by savepoint and container.
type ImageCaptureRequest struct {
	SnapshotID SavepointID
	Container  ContainerRef
}

// UpstreamImage is one image a compose file named, kept in a savepoint so a
// container can be rebuilt without its layer. It travels in the savepoint record,
// so its json tags are the wire contract.
type UpstreamImage struct {
	ID          ImageDigestID `json:"id"`                     // sha256 digest of the image config; the store key
	Refs        []string      `json:"refs,omitempty"`         // names, e.g. postgres:16
	RepoDigests []string      `json:"repo_digests,omitempty"` // registry digests, e.g. postgres@sha256:…
	SizeBytes   int64         `json:"size_bytes,omitempty"`   // image size as the daemon reports it
	StoredBytes int64         `json:"stored_bytes,omitempty"` // bytes this savepoint added; 0 when already stored
	// Stored is whether the store holds the bytes; false when neither the store nor
	// the daemon had them, and only the identity is recorded.
	Stored bool `json:"stored"`
}

// UpstreamImageCaptureRequest is one upstream image to keep. InDaemon is whether
// the daemon held it when the compose project was introspected.
type UpstreamImageCaptureRequest struct {
	Image    UpstreamImage
	InDaemon bool
}

// ImageCapture is one container's writable layer, committed and stored. It
// travels in the savepoint record, so its json tags are the wire contract.
type ImageCapture struct {
	Service     string `json:"service"`      // compose service name
	Replica     int    `json:"replica"`      // replica number, from 1
	ContainerID string `json:"container_id"` // the container committed
	// ContainerName is the committed container's name: what a rollback rebuilds
	// the replica under when compose numbered it otherwise. Kept, not derived from
	// the number: compose's naming depends on the project and its separator.
	ContainerName string `json:"container_name"`
	// CommittedImageID is the image `docker commit` made: the upstream plus the
	// writable layer. sha256 digest; what a rollback loads and tags.
	CommittedImageID ImageDigestID `json:"committed_image_id"`
	UpstreamImageID  ImageDigestID `json:"upstream_image_id,omitempty"`  // sha256 digest of the upstream's config
	UpstreamImageRef string        `json:"upstream_image_ref,omitempty"` // the upstream's name, e.g. postgres:16; never a digest
	SizeBytes        int64         `json:"size_bytes"`                   // the committed image's archive size
	// StoredBytes is what this capture added to the store; below SizeBytes when
	// deduplication found the bytes already there. A blob shared by replicas
	// captured in parallel counts toward whichever wrote it first, so only the
	// savepoint's total is stable.
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
// content, ImageRef is the docker reference the rebuilt compose project names it by. It
// travels in the rollback response, so its json tags are the wire contract.
type ImageRestore struct {
	Service       string        `json:"service"`
	Replica       int           `json:"replica"`
	ContainerName string        `json:"container_name"`
	ImageID       ImageDigestID `json:"image_id"`
	ImageRef      string        `json:"image_ref"`
}

// ReplicaIdentity is who a rebuilt container is to be: the name and the
// com.docker.compose.container-number a savepoint recorded for it.
type ReplicaIdentity struct {
	Name   string
	Number int
}
