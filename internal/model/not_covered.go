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

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

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
func (m NotCovered) IsEmpty() bool {
	return len(m.Mounts) == 0 && len(m.ServicesWithoutVolumes) == 0 && len(m.KilledContainers) == 0 &&
		len(m.ReplicaGaps) == 0 && len(m.UnresolvedImages) == 0 && len(m.WritableLayers) == 0 && len(m.Images) == 0
}

// Notice is one thing a savepoint does not hold, for a reader: what it is, and what to change
// when there is something to.
type Notice struct {
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	// Severe marks a notice that puts the data at risk; a reader lists it first.
	Severe bool `json:"severe,omitempty"`
}

// Notices spells out what is left out, one notice per entry: the severe first, then in the order
// of the fields.
func (m NotCovered) Notices() []Notice {
	var notices []Notice
	for _, killed := range m.KilledContainers {
		notices = append(notices, Notice{
			Message: fmt.Sprintf("%s was killed at the stop deadline: its volumes may hold a crash state",
				formatReplica(killed.Service, killed.Replica, formatShortID(killed.Container))),
			Hint:   "raise compose.stop_timeout or the service's stop_grace_period",
			Severe: true,
		})
	}
	for _, gap := range m.ReplicaGaps {
		notices = append(notices, Notice{
			Message: fmt.Sprintf("%s: replica %s gone, only %s held",
				gap.Service, formatInts(gap.Missing), formatInts(gap.Replicas)),
			Severe: true,
		})
	}
	for _, skipped := range m.Mounts {
		notices = append(notices, newMountNotice(skipped))
	}
	for _, service := range m.ServicesWithoutVolumes {
		message := fmt.Sprintf("%s has no volumes: only its writable layer is held", service)
		// A layer left out says so below; what is held of the service is then not this notice's to say.
		if slices.ContainsFunc(m.WritableLayers, func(layer SkippedWritableLayer) bool { return layer.Service == service }) {
			message = fmt.Sprintf("%s has no volumes", service)
		}
		notices = append(notices, Notice{
			Message: message,
		})
	}
	for _, unresolved := range m.UnresolvedImages {
		notices = append(notices, Notice{
			Message: fmt.Sprintf("%s runs on an image arcave cannot name%s: only its writable layer brings it back",
				formatReplica(unresolved.Service, unresolved.Replica, unresolved.Container), formatParens(unresolved.Image)),
		})
	}
	for _, layer := range m.WritableLayers {
		notices = append(notices, Notice{
			Message: fmt.Sprintf("%s: writable layer left out%s, a rollback creates it fresh from its image",
				formatReplica(layer.Service, layer.Replica, layer.Container), formatBy(layer.Detail)),
		})
	}
	for _, image := range m.Images {
		notices = append(notices, Notice{
			Message: fmt.Sprintf("%s: image %sleft out%s", image.Service, formatSpaced(image.Image), formatBy(image.Detail)),
		})
	}
	return notices
}

// newMountNotice says what a skipped mount is and why; an excluded one names the label or key.
func newMountNotice(skipped SkippedVolume) Notice {
	name := "volume " + skipped.Volume
	if skipped.Volume == "" {
		name = formatReplica(skipped.Service, skipped.Replica, "mount") + " " + skipped.Target
	}
	if skipped.Reason != SkipReasonExcluded {
		return Notice{Message: fmt.Sprintf("%s is not held: %s", name, skipped.Detail)}
	}
	hint := skipped.Detail + " leaves it out"
	if skipped.Detail == "" {
		hint = "an " + CaptureLabelCapture.String() + "=false or " + CaptureLabelMountsExclude.String() + " label leaves it out"
	}
	return Notice{
		Message: name + " is left out",
		Hint:    hint,
	}
}

// formatReplica names a replica as service-replica, a bare fallback when no service is known.
func formatReplica(service string, replica int, fallback string) string {
	switch {
	case service == "":
		return fallback
	case replica == 0:
		return service
	default:
		return service + "-" + strconv.Itoa(replica)
	}
}

// formatShortID cuts a container id to the 12 characters docker shows.
func formatShortID(id string) string {
	const shortIDLength = 12
	if len(id) > shortIDLength {
		return id[:shortIDLength]
	}
	return id
}

func formatInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

func formatParens(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func formatBy(source string) string {
	if source == "" {
		return ""
	}
	return " by " + source
}

func formatSpaced(s string) string {
	if s == "" {
		return ""
	}
	return s + " "
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
