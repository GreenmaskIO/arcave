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

import "time"

// The label a rollback stamps its safety snapshot with — the only thing telling
// it from an ordinary savepoint, and named by the service, the backends and both
// adapters alike.
const (
	SafetySnapshotLabel      = "arcave-safety-snapshot"
	SafetySnapshotLabelValue = "true"
)

// SpecFormatComposeYAML is the format of a savepoint's spec artifact: a compose
// document, loadable by compose's own loader.
const SpecFormatComposeYAML = "compose-yaml"

// Savepoint is the metadata of one consistent snapshot of every registered
// stateful volume. The per-volume bookkeeping lives with the SavepointRecord
// that embeds this type. Its json tags are the wire contract: single-savepoint
// command payloads marshal this type directly.
type Savepoint struct {
	ID          SavepointID       `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Stats       []SnapshotStat    `json:"stats,omitempty"` // one per snapshotted volume
	// Images is one capture per container whose writable layer was committed. It is
	// empty on records predating image capture and on workflows that take none.
	Images []ImageCapture `json:"images,omitempty"`
	// UpstreamImages is one entry per distinct image the compose file named, kept
	// whether or not a layer was captured over it.
	UpstreamImages []UpstreamImage `json:"upstream_images,omitempty"`
	// Spec names the compose spec committed beside this record. A savepoint without
	// one cannot be rolled back to: the compose project is rebuilt from the spec.
	Spec *SpecRef `json:"spec,omitempty"`
}

// SpecRef is the reference to a savepoint's compose spec artifact.
type SpecRef struct {
	Format string `json:"format"` // SpecFormatComposeYAML
}

// IsSafetySnapshot reports whether this record is a rollback's undo rather than
// a savepoint a user asked for.
func (s Savepoint) IsSafetySnapshot() bool {
	return s.Labels[SafetySnapshotLabel] == SafetySnapshotLabelValue
}
