package model

import "strings"

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

// The two prefixes keep the identity spaces apart: a volume named "db" and a
// service named "db" must not produce the same StorageID.
const (
	storageIDVolumePrefix  = "volume/"
	storageIDServicePrefix = "service/"
)

// namedStorageID identifies a named volume by the volume itself: its data
// outlives any one container, and several services may mount it.
func namedStorageID(dockerName string) string {
	return storageIDVolumePrefix + dockerName
}

// serviceStorageID identifies a bind or anonymous mount by service and target: a
// bind has no docker name, and an anonymous volume's is created per container.
func serviceStorageID(service, target string) string {
	return storageIDServicePrefix + service + "/" + strings.TrimPrefix(target, "/")
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
	// VolumeName is the resolved docker volume name — the reference to this
	// mount's Project.Volumes entry. Named volumes only.
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
	// Subpath exposes only a subdirectory of the volume through this mount:
	// reading or restoring through it would silently miss the rest.
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

type Service struct {
	Name    string
	Image   string
	Labels  map[string]string
	Volumes []ServiceVolume
}

// ServiceReference is one mount of a named volume by one service: who to stop,
// and where the data is reachable inside that service.
//
// The json tags are a contract: a restore picks the mount it writes through from
// the references the savepoint recorded.
type ServiceReference struct {
	Service string `json:"service"` // compose service name
	Target  string `json:"target"`  // mount path inside the container
	// ReadOnly and Subpath are what the restore planner picks a mount on. Two
	// mounts of one volume differing in either are two references, not a
	// duplicate.
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
// volumes by key.
type Project struct {
	Name     string
	Services []Service
	Volumes  []NamedVolume
}

type IntrospectionResult struct {
	Project Project
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
