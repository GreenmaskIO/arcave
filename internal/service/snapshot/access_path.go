package snapshot

import (
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
)

// accessPath picks the (service, target) pair this backend reaches a volume's
// data through. It is the backend's policy: CopyBackend moves bytes through a
// container that mounts the volume, so which mount only matters to a backend that
// works that way.
//
// A bind or anonymous volume *is* its one mount. A named one may have many, and
// the choice is the first reference with no subpath, preferring a writable one:
//
//   - Subpath disqualifies — capturing through it would silently store, and later
//     restore, a fraction of the volume.
//   - Read-only only deprioritises: it still reads, and refusing to capture such
//     a volume is a separate decision from picking a mount.
//
// UsedBy arrives sorted, so the choice is deterministic.
//
// An unusable volume cannot reach this far (core drops it), but it is an error
// rather than a panic anyway: the backend does not take a caller's word for its
// own invariant.
func accessPath(v model.Volume) (service, target string, err error) {
	switch {
	case v.Bind != nil:
		return v.Bind.Service, v.Bind.Target, nil
	case v.Anon != nil:
		return v.Anon.Service, v.Anon.Target, nil
	case v.Named != nil:
		return namedAccessPath(*v.Named)
	default:
		return "", "", fmt.Errorf("volume %s of type %s carries no addressing properties", v.ObjectID, v.Type)
	}
}

// namedAccessPath scans a named volume's mounts once, remembering the first
// usable one and returning early on the first usable *writable* one.
func namedAccessPath(named model.VolumePropertiesNamed) (service, target string, err error) {
	var fallback *model.ServiceReference
	for i, ref := range named.UsedBy {
		if ref.Subpath != "" {
			continue
		}
		if !ref.ReadOnly {
			return ref.Service, ref.Target, nil
		}
		if fallback == nil {
			fallback = &named.UsedBy[i]
		}
	}
	if fallback != nil {
		return fallback.Service, fallback.Target, nil
	}
	return "", "", fmt.Errorf("named volume %s has no mount to reach its data through", named.VolumeName)
}
