package model

import "path/filepath"

// MountSourceType says how Source is read.
type MountSourceType string

const (
	MountSourceVolume MountSourceType = "volume" // docker volume name
	MountSourceBind   MountSourceType = "bind"   // host path
)

// MountSource is what a container mounts to reach a volume's data — docker's own
// `type=…,source=…` pair, minus the target the caller picks. The type is not
// decoration: only a docker volume can be asked whether it exists.
type MountSource struct {
	Type   MountSourceType
	Source string
}

// MountSource answers from the compose project alone. ok is false for an
// anonymous volume, whose docker name is created per container — only the daemon
// knows it — and for a bind whose source is missing or relative; both fall back
// to LookupMount.
func (v Volume) MountSource() (MountSource, bool) {
	switch {
	case v.Named != nil:
		if v.Named.VolumeName == "" {
			return MountSource{}, false
		}
		return MountSource{Type: MountSourceVolume, Source: v.Named.VolumeName}, true
	case v.Bind != nil:
		// compose-go resolves a bind source against the project directory, so a
		// relative one means the record never went through it.
		if !filepath.IsAbs(v.Bind.Source) {
			return MountSource{}, false
		}
		return MountSource{Type: MountSourceBind, Source: v.Bind.Source}, true
	default:
		return MountSource{}, false
	}
}

// LookupMount is the mount the daemon has to be asked through when MountSource
// cannot answer. ok is false for a Volume carrying no mount of its own.
func (v Volume) LookupMount() (service, target string, ok bool) {
	switch {
	case v.Anon != nil:
		return v.Anon.Service, v.Anon.Target, true
	case v.Bind != nil:
		return v.Bind.Service, v.Bind.Target, true
	default:
		return "", "", false
	}
}
