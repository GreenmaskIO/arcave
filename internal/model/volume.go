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

// Volume is the unit of backup, flattened from the introspection result.
// Exactly one of Named/Bind/Anon is non-nil, matching Type: addressing is
// type-specific, so each arm carries only the fields its type has, and reading
// the wrong one is a nil deref rather than a silent empty string.
//
// The arms hold no per-mount labels: those are input to the decision, and by the
// time a Volume exists Backend is already assigned.
//
// The json tags are a contract: a Volume is stored in every savepoint record and
// reported by every rollback.
type Volume struct {
	ObjectID string `json:"object_id"` // stable identity: ServiceVolume.ObjectID() / NamedVolume.ObjectID()
	// ComposeProject is the project name at capture time — how docker locates
	// the compose project's containers.
	ComposeProject string              `json:"compose_project"`
	Type           VolumeType          `json:"type"`
	Backend        SnapshotBackendKind `json:"backend"`

	Named *VolumePropertiesNamed `json:"named,omitempty"`
	Bind  *VolumePropertiesBind  `json:"bind,omitempty"`
	Anon  *VolumePropertiesAnon  `json:"anon,omitempty"`
}

// VolumePropertiesNamed addresses a named volume by its docker name. UsedBy is
// every mount of it — who to stop, and what a savepoint of it covers — and not
// a way in: the volume is reached by name, mounted or not.
type VolumePropertiesNamed struct {
	VolumeName string             `json:"volume_name"`
	UsedBy     []ServiceReference `json:"used_by,omitempty"`
}

// VolumePropertiesBind addresses a bind mount: the service declaring it and the
// host path the data lives at.
type VolumePropertiesBind struct {
	Service  string `json:"service"`
	Target   string `json:"target"`
	Source   string `json:"source,omitempty"` // host path the data lives at, verbatim compose "source"
	ReadOnly bool   `json:"read_only,omitempty"`
}

// VolumePropertiesAnon addresses an anonymous volume — reachable only through
// its one mount: the docker name is created per container and is not stable, so
// this is the one arm that has to ask the daemon.
type VolumePropertiesAnon struct {
	Service  string `json:"service"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
	// Subpath is carried for the record and read by nobody: the daemon refuses
	// an anonymous volume with a subpath outright.
	Subpath string `json:"subpath,omitempty"`
}
