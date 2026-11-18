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
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"

	"github.com/greenmaskio/arcave/internal/model"
)

// An ObjectID is one path segment: it is the name of a directory in a store and
// the name of a repository on the restic wire, and both want a segment rather
// than a tree. Every prefix opens with "volume-" — everything addressed here is
// a volume — and the second word says how it is addressed, which keeps the
// identity spaces apart: a volume named "db" and a service named "db" must not
// produce the same id.
const (
	storageIDNamedPrefix   = "volume-named-"
	storageIDServicePrefix = "volume-service-"
	storageIDAnonPrefix    = "volume-anon-"
)

// IsAbsent reports whether the daemon holds no container of the compose project:
// it was never started, or was taken down.
func IsAbsent(introspection IntrospectionResult) bool {
	return len(introspection.Project.Services) == 0
}

// OwnedVolumes are the docker names of the volumes the compose project owns.
func OwnedVolumes(introspection IntrospectionResult) map[string]bool {
	owned := map[string]bool{}
	for _, v := range introspection.Project.Volumes {
		if !v.External {
			owned[v.Name] = true
		}
	}
	return owned
}

// NewAnonymousVolumeNames indexes the docker names of the compose project's anonymous volumes by
// the identity their data is captured under.
func NewAnonymousVolumeNames(introspection IntrospectionResult) map[string]string {
	names := map[string]string{}
	for _, svc := range introspection.Project.Services {
		for _, mount := range svc.Volumes {
			if mount.Type == model.VolumeTypeAnon && mount.VolumeName != "" {
				names[mount.ObjectID(svc.Name)] = mount.VolumeName
			}
		}
	}
	return names
}

// VolumesOf projects a savepoint's stats back to volumes.
func VolumesOf(savepoint model.Savepoint) []model.Volume {
	volumes := make([]model.Volume, 0, len(savepoint.Stats))
	for _, stat := range savepoint.Stats {
		volumes = append(volumes, stat.Volume)
	}
	return volumes
}

// ServiceVolume is one mount declared by one service — a fact, not a decision.
// Every mount compose declares is reported, whatever its type.
//
// A named mount is only a reference to the project's NamedVolume, the unit of
// backup for volume data; VolumeName joins the two levels, so a volume three
// services mount is not captured three times.
type ServiceVolume struct {
	Type model.VolumeType
	// RawType is the compose mount type verbatim ("volume", "bind", "tmpfs", …).
	RawType string
	// Source is the compose "source" verbatim: host path (bind), volume key
	// (named), image reference (image). Empty for anonymous and tmpfs.
	Source string
	// Target is the mount path inside the container.
	Target string
	// VolumeName is the resolved docker volume name: a named mount's Project.Volumes
	// entry, or the name the daemon generated for an anonymous one. Empty otherwise.
	VolumeName string
	// ReadOnly does not affect reading the data, but a restore cannot be written
	// through such a mount.
	ReadOnly bool
	// Replica is the number of the replica an anonymous volume belongs to; 0 for
	// every other type, which the replicas of a service share.
	Replica int
	// InheritedFrom is the id of the container this mount is inherited from through
	// volumes_from; empty for a mount the container requested itself.
	InheritedFrom string

	// At most one of these is non-nil, matching Type (Volume serves both named and
	// anon); nil when compose declared no options of that type. Verbatim facts.
	Bind   *BindMount
	Volume *VolumeMount
	Tmpfs  *TmpfsMount
	Image  *ImageMount
}

// ObjectID is the stable identity of this mount's data — what a backend renders
// its storage key from. A named volume returns its NamedVolume's id, which is
// how the two levels join; no other type is ever a unit of backup, so none has
// an id.
//
// A rename produces a new identity, deliberately: docker creates an empty volume on
// a rename, so an old backup must not map onto it.
func (m ServiceVolume) ObjectID(service string) string {
	switch m.Type {
	case model.VolumeTypeNamed:
		return namedStorageID(m.VolumeName)
	case model.VolumeTypeBind:
		return serviceStorageID(service, m.Target)
	case model.VolumeTypeAnon:
		return anonStorageID(service, m.Replica, m.Target)
	default:
		return ""
	}
}

// BindMount is the bind-specific options of one mount. Report-only: none of it
// changes how arcave reads or writes the data.
type BindMount struct {
	SELinux        string // "z" (shared) or "Z" (private unshared)
	Propagation    string // rprivate, shared, …
	CreateHostPath bool
	Recursive      string
}

// VolumeMount is the volume-specific options of one named or anonymous mount.
// Labels here are per-mount: for an anonymous volume this is the only place
// labels can be declared, so a label-driven policy has no other channel to it.
type VolumeMount struct {
	NoCopy bool // affects only the volume's first population from the image
	// Subpath exposes only a subdirectory of the volume through this mount.
	// Report-only: a backend addresses the volume itself, so a savepoint covers
	// all of it and a rollback puts all of it back, whatever a service sees.
	Subpath string
	Labels  map[string]string
}

// TmpfsMount is the tmpfs-specific options of one mount.
type TmpfsMount struct {
	SizeBytes int64
	Mode      uint32
}

// ImageMount is the image-specific options of one mount.
type ImageMount struct {
	Subpath string
}

// Service is one service of the project: what it declares, and the containers
// the daemon holds for it.
type Service struct {
	Name   string
	Image  string
	Labels map[string]string
	// Volumes are the first replica's mounts plus every later replica's anonymous
	// volumes, sorted by (Target, Replica).
	Volumes []ServiceVolume
	// Containers are the service's replicas, sorted by Number. Empty for a
	// service the daemon holds nothing for.
	Containers []Container
}

// Container is one replica of a service, as the daemon holds it: the raw material
// the compose spec is rebuilt from.
type Container struct {
	ID   string
	Name string
	// Number is com.docker.compose.container-number: which replica of the
	// service this is, counted from 1.
	Number int
	Labels map[string]string
	// Replaces is the name of the container compose recreated this one over; empty for one it
	// created anew.
	Replaces string
	// Inspect is what `docker container inspect` reports, verbatim.
	Inspect container.InspectResponse
}

// ImageID is the id of the image the container runs on; empty when the daemon
// reported none or one that is no digest.
func (m Container) ImageID() model.ImageDigestID {
	id, err := model.ParseImageDigestID(m.Inspect.Image)
	if err != nil {
		return ""
	}
	return id
}

// Network is one network of the project. Nothing arcave captures lives in one;
// it is reported because a compose project cannot be started again without it.
type Network struct {
	// Key is the network's name inside the compose project, Name its name in
	// docker — the same two levels NamedVolume documents.
	Key  string
	Name string
	// Inspect is what `docker network inspect` reports, verbatim.
	Inspect network.Inspect
}

// NamedVolume is a named volume of the project — the unit of backup for volume
// data, addressed once however many services mount it. A volume declared but
// mounted by nobody still appears, with no UsedBy: docker creates it, so it can
// hold data.
type NamedVolume struct {
	// Key is the volume's name inside the compose project — its key in the
	// volumes: section, and what a mount names as its source. Name is its name in
	// docker, which compose resolves:
	//
	//	volumes:
	//	  pgdata:            # Key "pgdata",  Name "demo_pgdata" in project demo
	//	  cache:
	//	    name: my_cache   # Key "cache",   Name "my_cache"
	//	  shared:
	//	    external: true   # Key "shared",  Name "shared"
	//
	// Only Name identifies the data — two projects declaring "pgdata" hold two
	// different volumes — so ObjectID is built from it. Key joins the two levels
	// of the introspection result: it is a named ServiceVolume's Source.
	Key  string
	Name string
	// External marks a volume the project references but does not own. Reported,
	// not acted on: its data is as restorable as any other.
	External bool
	// UsedBy names the mounts of this volume, sorted by (Service, Target) and by
	// declaration order within a tie — who to stop to snapshot it, and where to
	// reach the data through each. A service mounting it at two paths appears
	// twice.
	UsedBy []model.ServiceReference
	// Labels, Driver and DriverOpts are the volumes: section verbatim — input to
	// whoever decides what a savepoint captures, interpreted by nobody here.
	Labels     map[string]string
	Driver     string
	DriverOpts map[string]string
}

// ObjectID is the id a named ServiceVolume referencing this volume also returns.
func (m NamedVolume) ObjectID() string {
	return namedStorageID(m.Name)
}

// Project is the compose project as arcave sees it. Services are sorted by name,
// volumes and networks by key.
type Project struct {
	Name     string
	Services []Service
	Volumes  []NamedVolume
	Networks []Network
}

// IntrospectionResult is everything a workflow reads from the daemon about a compose project, taken
// in one pass before anything is stopped. Daemon answers are verbatim.
type IntrospectionResult struct {
	Project Project
	// Images are the images the compose project's containers run on and the base images those name
	// by SystemLabelBaseImageID, keyed by id, verbatim.
	Images map[model.ImageDigestID]image.InspectResponse
	// Volumes are the volumes the project owns and every other volume a container mounts,
	// keyed by docker name, verbatim.
	Volumes map[string]volume.Volume
	// Referenced are the containers outside the project a replica names in container: network,
	// ipc or pid mode or in volumes_from, keyed by id, verbatim. One the daemon no longer has is absent.
	Referenced map[string]container.InspectResponse
}

// namedStorageID identifies a named volume by the volume itself: its data
// outlives any one container, and several services may mount it.
func namedStorageID(dockerName string) string {
	return storageIDNamedPrefix + dockerName
}

// serviceStorageID identifies a bind by service and target: a bind has no docker
// name, and every replica of the service mounts the same host path.
//
// The target is flattened into the segment, so two identities can render the
// same id ({app, /var/lib} and {app-var, /lib}). Deciding what to do about that
// needs the whole project's set of volumes, and belongs where that set is known.
func serviceStorageID(service, target string) string {
	return storageIDServicePrefix + service + "-" + flattenTarget(target)
}

// anonStorageID identifies an anonymous volume by service, replica and target:
// docker creates one per container, so every replica holds its own data.
func anonStorageID(service string, replica int, target string) string {
	return storageIDAnonPrefix + service + "-" + strconv.Itoa(replica) + "-" + flattenTarget(target)
}

// flattenTarget renders a mount path as part of one path segment.
func flattenTarget(target string) string {
	return strings.ReplaceAll(strings.TrimPrefix(target, "/"), "/", "-")
}
