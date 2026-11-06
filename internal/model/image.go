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

// Image is one image a compose file named, kept in a savepoint so a
// container can be rebuilt without its writable layer. It travels in the savepoint record,
// so its json tags are the wire contract.
type Image struct {
	ID          ImageDigestID `json:"id"`                     // sha256 digest of the image config; the store key
	Refs        []string      `json:"refs,omitempty"`         // names, e.g. postgres:16
	RepoDigests []string      `json:"repo_digests,omitempty"` // registry digests, e.g. postgres@sha256:…
	SizeBytes   int64         `json:"size_bytes,omitempty"`   // image size as the daemon reports it
	StoredBytes int64         `json:"stored_bytes,omitempty"` // bytes this savepoint added; 0 when already stored
	// Stored is whether the store holds the bytes; false when neither the store nor
	// the daemon had them, or the daemon failed to hand them over, and only the
	// identity is recorded.
	Stored bool `json:"stored"`
}

// ImageCaptureRequest is one image to keep. ExistsInDaemon is whether the Docker
// daemon still held it when the compose project was introspected: only then can its
// bytes be stored.
type ImageCaptureRequest struct {
	Image          Image
	ExistsInDaemon bool
}
