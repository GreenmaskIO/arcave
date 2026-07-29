package tarcopy

import (
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
)

// captureMountRef is the one mount a volume is captured through: the service
// whose container is entered, and the path the volume sits at inside it.
type captureMountRef struct {
	service string
	target  string
}

// captureMount picks the mount to reach a volume's data through. A bind or anon
// volume is its one mount; for a named one a subpath mount disqualifies (it
// would capture a fraction of the volume), read-only only deprioritises. UsedBy
// arrives sorted, so the choice is deterministic.
func captureMount(v model.Volume) (captureMountRef, error) {
	switch {
	case v.Bind != nil:
		return captureMountRef{service: v.Bind.Service, target: v.Bind.Target}, nil
	case v.Anon != nil:
		return captureMountRef{service: v.Anon.Service, target: v.Anon.Target}, nil
	case v.Named != nil:
		return namedCaptureMount(*v.Named)
	default:
		return captureMountRef{}, fmt.Errorf("volume %s of type %s carries no addressing properties", v.ObjectID, v.Type)
	}
}

// namedCaptureMount returns the first no-subpath mount, preferring a writable one.
func namedCaptureMount(named model.VolumePropertiesNamed) (captureMountRef, error) {
	var fallback *model.ServiceReference
	for i, ref := range named.UsedBy {
		if ref.Subpath != "" {
			continue
		}
		if !ref.ReadOnly {
			return captureMountRef{service: ref.Service, target: ref.Target}, nil
		}
		if fallback == nil {
			fallback = &named.UsedBy[i]
		}
	}
	if fallback != nil {
		return captureMountRef{service: fallback.Service, target: fallback.Target}, nil
	}
	return captureMountRef{}, fmt.Errorf("named volume %s has no mount to reach its data through", named.VolumeName)
}
