package model

import "strings"

// VolumeType is arcave's classification of a mount. Compose says "volume" for
// both a named and an anonymous volume; arcave tells them apart because they are
// backed up at different levels.
type VolumeType string

const (
	VolumeTypeBind  VolumeType = "bind"
	VolumeTypeNamed VolumeType = "named"
	VolumeTypeAnon  VolumeType = "anon"
	// VolumeTypeUnsupported is a mount arcave cannot snapshot (tmpfs, npipe,
	// cluster, image). RawType carries the diagnosis.
	VolumeTypeUnsupported VolumeType = "unsupported"
)

// SnapshotBackend names the mechanism that captures a volume. Introspect only
// proposes a default; the consumer decides.
type SnapshotBackend string

const (
	SnapshotBackendCDC      SnapshotBackend = "cdc"
	SnapshotBackendFullCopy SnapshotBackend = "full-copy"
	SnapshotBackendUnknown  SnapshotBackend = "unknown"
)

// The two prefixes keep the identity spaces apart: a volume named "db" and a
// service named "db" must not mint the same StorageID.
const (
	storageIDVolumePrefix  = "volume/"
	storageIDServicePrefix = "service/"
)

// namedStorageID identifies a named volume by the volume itself: its data
// outlives any one container, and several services may mount it.
func namedStorageID(dockerName string) string {
	return storageIDVolumePrefix + dockerName
}

// serviceStorageID identifies a bind or anonymous mount by service and target —
// a bind has no docker name, and an anonymous volume's is a hash docker mints at
// container creation, so neither is stable across a recreate.
func serviceStorageID(service, target string) string {
	return storageIDServicePrefix + service + "/" + strings.TrimPrefix(target, "/")
}

// ServiceVolume is one mount declared by one service. A mount arcave cannot
// handle is not dropped: it is Type VolumeTypeUnsupported.
//
// NeedToBackup and Backend live on the unit of backup, which for a named volume
// is the project's NamedVolume — so a named entry leaves both zero and carries
// VolumeName as the reference to it. Otherwise a volume shared by three services
// would be captured three times.
//
//	bind, anon  → NeedToBackup true,  Backend full-copy
//	named       → zero; see NamedVolume
//	unsupported → NeedToBackup false, Backend unknown
type ServiceVolume struct {
	Type VolumeType
	// RawType is the compose mount type verbatim ("volume", "bind", "tmpfs", …).
	RawType string
	// Source is the host path (bind) or the compose volume key (named); empty for
	// anonymous and unsupported.
	Source string
	// Target is the mount path inside the container.
	Target string
	// VolumeName is the resolved Docker volume name, and the reference to this
	// mount's Project.Volumes entry. Named volumes only.
	VolumeName   string
	NeedToBackup bool
	Backend      SnapshotBackend
}

// ObjectID is the stable identity of this mount's data — the key to iterate by
// and the basis of the storage key a backend renders (identity, not a path).
// A named volume returns its NamedVolume's id, which is how the two levels join;
// an unsupported mount has none.
//
// A rename therefore mints a new identity, deliberately: docker creates an empty
// volume on a rename too, so an old backup must not map onto it. A restore into
// a renamed project fails preflight as a missing archive.
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
	Key  string // compose key ("pgdata")
	Name string // resolved Docker name ("demo_pgdata", or an explicit name:)
	// External marks a volume the project references but does not own. Reported,
	// not acted on: its data is as restorable as any other.
	External bool
	// UsedBy names the services mounting it, sorted — who to stop to snapshot it.
	UsedBy       []string
	NeedToBackup bool
	Backend      SnapshotBackend
}

// StorageID is the id a named ServiceVolume referencing this volume also returns.
func (v NamedVolume) StorageID() string {
	return namedStorageID(v.Name)
}

// Project is the compose project as arcave sees it. Services are sorted by name,
// volumes by key.
type Project struct {
	Name     string
	Services []Service
	Volumes  []NamedVolume
}

type InspectionResult struct {
	Project Project
}
