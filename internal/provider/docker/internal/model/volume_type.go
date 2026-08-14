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
	SnapshotBackendCDC     SnapshotBackendKind = "cdc"
	SnapshotBackendCopy    SnapshotBackendKind = "copy"
	SnapshotBackendUnknown SnapshotBackendKind = "unknown"
)

// ServiceReference is one mount of a named volume by one service: who to stop,
// and where it shows up inside. The json tags travel in every savepoint record.
type ServiceReference struct {
	Service string `json:"service"` // compose service name
	Target  string `json:"target"`  // mount path inside the container
	// ReadOnly and Subpath describe this mount, not the volume: two mounts of one
	// volume differing in either are two references, not a duplicate.
	ReadOnly bool   `json:"read_only,omitempty"`
	Subpath  string `json:"subpath,omitempty"`
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
