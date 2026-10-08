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
	// UnresolvedUpstreams are the containers whose upstream image cannot be named:
	// their layer is held, the image under it is not.
	UnresolvedUpstreams []UnresolvedUpstream `json:"unresolved_upstreams,omitempty"`
}

// IsEmpty reports whether nothing is left out.
func (n NotCovered) IsEmpty() bool {
	return len(n.Mounts) == 0 && len(n.ServicesWithoutVolumes) == 0 && len(n.KilledContainers) == 0 &&
		len(n.ReplicaGaps) == 0 && len(n.UnresolvedUpstreams) == 0
}

// SkippedVolume is a mount the backup decision did not take. Not deduplicated:
// two tmpfs mounts are two skips.
type SkippedVolume struct {
	Service string     `json:"service,omitempty"`
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

// UnresolvedUpstream is a container whose upstream image cannot be named.
type UnresolvedUpstream struct {
	Service   string `json:"service"`
	Replica   int    `json:"replica"`
	Container string `json:"container"`
	Image     string `json:"image,omitempty"` // what Config.Image reads
}
