package model

import (
	"strings"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
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
func serviceStorageID(service, target string) string {
	return storageIDServicePrefix + service + "-" + strings.ReplaceAll(strings.TrimPrefix(target, "/"), "/", "-")
}

// ServiceVolume is one mount declared by one service. A named mount only
// references the project's NamedVolume, the unit of backup.
type ServiceVolume struct {
	Type dockermodel.VolumeType
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

// ObjectID is the stable identity of this mount's data. A rename yields a new
// one: docker creates an empty volume on a rename.
func (v ServiceVolume) ObjectID(service string) string {
	switch v.Type {
	case dockermodel.VolumeTypeNamed:
		return namedStorageID(v.VolumeName)
	case dockermodel.VolumeTypeBind, dockermodel.VolumeTypeAnon:
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

type Service struct {
	Name    string
	Image   string
	Labels  map[string]string
	Volumes []ServiceVolume
}

// NamedVolume is a named volume of the project — the unit of backup for volume
// data, addressed once however many services mount it. A volume declared but
// mounted by nobody still appears, with no UsedBy: docker creates it, so it can
// hold data.
type NamedVolume struct {
	// Key is the volume's key in the compose volumes: section. Name is what compose
	// resolves it to in docker, and the only one that identifies the data.
	Key  string
	Name string
	// External marks a volume the project references but does not own. Reported,
	// not acted on: its data is as restorable as any other.
	External bool
	// UsedBy names the mounts of this volume, sorted by (Service, Target) and by
	// declaration order within a tie — who to stop to snapshot it, and where to
	// reach the data through each. A service mounting it at two paths appears
	// twice.
	UsedBy []dockermodel.ServiceReference
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

// ComposeProject is the compose project as arcave sees it, services sorted by
// name and volumes by key.
type ComposeProject struct {
	Name     string
	Services []Service
	Volumes  []NamedVolume
}

type IntrospectionResult struct {
	Project ComposeProject
}
