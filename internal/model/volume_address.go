package model

import "path/filepath"

// VolumeAddressKind says how Source is read.
type VolumeAddressKind string

const (
	VolumeAddressVolume VolumeAddressKind = "volume" // docker volume name
	VolumeAddressBind   VolumeAddressKind = "bind"   // host path
)

// VolumeAddress is what a container binds to reach a volume's data. The kind is
// not decoration: only a docker volume can be asked whether it exists.
type VolumeAddress struct {
	Kind   VolumeAddressKind
	Source string
}

// Address answers from the compose project alone. ok is false for an anonymous
// volume, whose docker name is created per container — only the daemon knows it —
// and for a bind whose source is missing or relative; both fall back to
// LookupMount.
func (v Volume) Address() (VolumeAddress, bool) {
	switch {
	case v.Named != nil:
		if v.Named.VolumeName == "" {
			return VolumeAddress{}, false
		}
		return VolumeAddress{Kind: VolumeAddressVolume, Source: v.Named.VolumeName}, true
	case v.Bind != nil:
		// compose-go resolves a bind source against the project directory, so a
		// relative one means the record never went through it.
		if !filepath.IsAbs(v.Bind.Source) {
			return VolumeAddress{}, false
		}
		return VolumeAddress{Kind: VolumeAddressBind, Source: v.Bind.Source}, true
	default:
		return VolumeAddress{}, false
	}
}

// LookupMount is the mount the daemon has to be asked through when Address
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
