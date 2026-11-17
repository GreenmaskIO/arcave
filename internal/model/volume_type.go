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

// VolumeType is arcave's classification of a mount: a refinement of the compose
// mount type, not a parallel taxonomy. Compose's "volume" splits into named and
// anon, which are backed up at different levels; every other type maps one to
// one and keeps its compose name. Whether a mount is backed up is decided
// elsewhere.
type VolumeType string

const (
	VolumeTypeBind    VolumeType = "bind"
	VolumeTypeNamed   VolumeType = "named"
	VolumeTypeAnon    VolumeType = "anon"
	VolumeTypeTmpfs   VolumeType = "tmpfs"
	VolumeTypeNPipe   VolumeType = "npipe"
	VolumeTypeCluster VolumeType = "cluster"
	VolumeTypeImage   VolumeType = "image"
	// VolumeTypeUnknown is a compose mount type arcave does not know; RawType is
	// the diagnosis.
	VolumeTypeUnknown VolumeType = "unknown"
)

// SnapshotBackendKind names the mechanism that captures a volume. Assigned where
// a savepoint's units are decided, never by introspection. Kind is not noise: it
// is what keeps this apart from VolumeSnapshotBackend, the instance itself.
type SnapshotBackendKind string

const (
	SnapshotBackendCDC      SnapshotBackendKind = "cdc"
	SnapshotBackendFullCopy SnapshotBackendKind = "fullcopy"
	SnapshotBackendUnknown  SnapshotBackendKind = "unknown"
)

// ServiceReference is one mount of a named volume by one service: who to stop,
// and where the volume shows up inside that service.
//
// The json tags are a contract: the references travel in every savepoint record.
// No backend reads them to reach the data — a volume is addressed by its docker
// name — so this is what a savepoint covers, reported, not how it is reached.
type ServiceReference struct {
	Service string `json:"service"` // compose service name
	Target  string `json:"target"`  // mount path inside the container
	// ReadOnly and Subpath describe this mount, not the volume: two mounts of one
	// volume differing in either are two references, not a duplicate.
	ReadOnly bool   `json:"read_only,omitempty"`
	Subpath  string `json:"subpath,omitempty"`
}
