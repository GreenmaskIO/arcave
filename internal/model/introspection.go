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
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
)

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
	SnapshotBackendCDC     SnapshotBackendKind = "cdc"
	SnapshotBackendCopy    SnapshotBackendKind = "copy"
	SnapshotBackendUnknown SnapshotBackendKind = "unknown"
)

// An ObjectID is one path segment: it is the name of a directory in a store and
// the name of a repository on the restic wire, and both want a segment rather
// than a tree. Both prefixes open with "volume-" — everything addressed here is
// a volume — and the second word says how it is addressed, which keeps the
// identity spaces apart: a volume named "db" and a service named "db" must not
// produce the same id.
const (
	storageIDNamedPrefix   = "volume-named-"
	storageIDServicePrefix = "volume-service-"
)

// namedStorageID identifies a named volume by the volume itself: its data
// outlives any one container, and several services may mount it.
func namedStorageID(dockerName string) string {
	return storageIDNamedPrefix + dockerName
}

// serviceStorageID identifies a bind or anonymous mount by service and target: a
// bind has no docker name, and an anonymous volume's is created per container.
//
// The target is flattened into the segment, so two identities can render the
// same id ({app, /var/lib} and {app-var, /lib}). Deciding what to do about that
// needs the whole project's set of volumes, and belongs where that set is known.
func serviceStorageID(service, target string) string {
	return storageIDServicePrefix + service + "-" + strings.ReplaceAll(strings.TrimPrefix(target, "/"), "/", "-")
}

// ServiceVolume is one mount declared by one service — a fact, not a decision.
// Every mount compose declares is reported, whatever its type.
//
// A named mount is only a reference to the project's NamedVolume, the unit of
// backup for volume data; VolumeName joins the two levels, so a volume three
// services mount is not captured three times.
type ServiceVolume struct {
	Type VolumeType
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
func (v ServiceVolume) ObjectID(service string) string {
	switch v.Type {
	case VolumeTypeNamed:
		return namedStorageID(v.VolumeName)
	case VolumeTypeBind, VolumeTypeAnon:
		return serviceStorageID(service, v.Target)
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
	Name    string
	Image   string
	Labels  map[string]string
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
	// Inspect is what `docker container inspect` reports, verbatim.
	Inspect container.InspectResponse
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
	UsedBy []ServiceReference
	// Labels, Driver and DriverOpts are the volumes: section verbatim — input to
	// whoever decides what a savepoint captures, interpreted by nobody here.
	Labels     map[string]string
	Driver     string
	DriverOpts map[string]string
}

// ObjectID is the id a named ServiceVolume referencing this volume also returns.
func (v NamedVolume) ObjectID() string {
	return namedStorageID(v.Name)
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
	// Daemon is what `docker info` reports.
	Daemon system.Info
	// Images are the images the compose project's containers run on and the base images those name
	// by BaseImageIDLabel, keyed by id, verbatim.
	Images map[ImageDigestID]image.InspectResponse
	// Volumes are the volumes the project owns and every other volume a container mounts,
	// keyed by docker name, verbatim.
	Volumes map[string]volume.Volume
}

// SkippedVolume is a mount the backup decision did not take, reported so callers
// can tell the user which state a savepoint does not cover. Not deduplicated:
// two tmpfs mounts are two skips.
type SkippedVolume struct {
	Service string `json:"service,omitempty"`
	Target  string `json:"target,omitempty"`
	RawType string `json:"raw_type,omitempty"` // the compose mount type — the diagnosis
	Reason  string `json:"reason"`
}
