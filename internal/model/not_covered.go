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

// SkipReason says why a mount is not a unit of backup. Its values are the wire
// contract an agent matches on; the prose goes to SkippedVolume.Detail.
type SkipReason string

const (
	SkipReasonTmpfs                SkipReason = "tmpfs"
	SkipReasonNPipe                SkipReason = "npipe"
	SkipReasonImage                SkipReason = "image"
	SkipReasonCluster              SkipReason = "cluster"
	SkipReasonUnknownType          SkipReason = "unknown_type"
	SkipReasonInheritedFromOutside SkipReason = "inherited_from_outside"
	// SkipReasonExcluded is a unit an arcave.* label leaves out.
	SkipReasonExcluded SkipReason = "excluded"
)

// NotCovered is the state a savepoint does not hold. The zero value covers everything.
type NotCovered struct {
	// Mounts are the mounts the backup decision did not take.
	Mounts []SkippedVolume `json:"mounts,omitempty"`
	// ServicesWithoutVolumes are the services no captured volume belongs to; only
	// their writable layer is held.
	ServicesWithoutVolumes []string `json:"services_without_volumes,omitempty"`
	// KilledContainers were SIGKILLed at the stop deadline: their volumes may hold
	// a crash state.
	KilledContainers []KilledContainer `json:"killed_containers,omitempty"`
	// ReplicaGaps are the services missing a replica below their highest one.
	ReplicaGaps []ReplicaGap `json:"replica_gaps,omitempty"`
	// UnresolvedImages are the containers whose image cannot be named: a rollback
	// cannot rebuild them from it, so only a held writable layer brings them back.
	UnresolvedImages []UnresolvedImage `json:"unresolved_images,omitempty"`
	// WritableLayers are the replicas whose writable layer a label leaves out.
	WritableLayers []SkippedWritableLayer `json:"writable_layers,omitempty"`
	// Images are the services whose image a label leaves out.
	Images []SkippedImage `json:"images,omitempty"`
}

// IsEmpty reports whether nothing is left out.
func (n NotCovered) IsEmpty() bool {
	return len(n.Mounts) == 0 && len(n.ServicesWithoutVolumes) == 0 && len(n.KilledContainers) == 0 &&
		len(n.ReplicaGaps) == 0 && len(n.UnresolvedImages) == 0 && len(n.WritableLayers) == 0 && len(n.Images) == 0
}

// SkippedVolume is a mount the backup decision did not take. Not deduplicated:
// two tmpfs mounts are two skips.
type SkippedVolume struct {
	// Volume is the docker name of a named volume left out whole; Service and
	// Target name a mount.
	Volume  string     `json:"volume,omitempty"`
	Service string     `json:"service,omitempty"`
	Replica int        `json:"replica,omitempty"` // an anonymous volume's replica
	Target  string     `json:"target,omitempty"`
	RawType string     `json:"raw_type,omitempty"` // the compose mount type
	Reason  SkipReason `json:"reason"`
	Detail  string     `json:"detail,omitempty"`
}

// ReplicaGap is a service whose replicas are not numbered 1..N: only the
// remaining ones are held, and a rollback brings back exactly these.
type ReplicaGap struct {
	Service  string `json:"service"`
	Replicas []int  `json:"replicas"`
	Missing  []int  `json:"missing"`
}

// KilledContainer is a replica the stop deadline SIGKILLed. For a container the
// introspection did not see, Service and Replica are empty and Container is its id.
type KilledContainer struct {
	Service   string `json:"service,omitempty"`
	Replica   int    `json:"replica,omitempty"`
	Container string `json:"container"`
}

// UnresolvedImage is a container whose image cannot be named, whether or not its
// writable layer is held.
type UnresolvedImage struct {
	Service   string `json:"service"`
	Replica   int    `json:"replica"`
	Container string `json:"container"`
	Image     string `json:"image,omitempty"` // what Config.Image reads
}

// SkippedWritableLayer is a replica whose writable layer is left out; a rollback creates
// it fresh from its image.
type SkippedWritableLayer struct {
	Service   string     `json:"service"`
	Replica   int        `json:"replica"`
	Container string     `json:"container"`
	Reason    SkipReason `json:"reason"`
	Detail    string     `json:"detail,omitempty"`
}

// SkippedImage is a service whose image bytes are left out.
type SkippedImage struct {
	Service string     `json:"service"`
	Image   string     `json:"image,omitempty"` // the image's reference
	Reason  SkipReason `json:"reason"`
	Detail  string     `json:"detail,omitempty"`
}
