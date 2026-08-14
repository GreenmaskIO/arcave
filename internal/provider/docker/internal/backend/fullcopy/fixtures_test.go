package fullcopy

import (
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
	stackmodel "github.com/greenmaskio/arcave/internal/provider/docker/stack/model"
)

const testComposeProject = "demo"

func namedVolume(dockerName string, usedBy ...dockermodel.ServiceReference) dockermodel.Volume {
	return dockermodel.Volume{
		ObjectID:       stackmodel.NamedVolume{Name: dockerName}.ObjectID(),
		ComposeProject: testComposeProject,
		Type:           dockermodel.VolumeTypeNamed,
		Backend:        dockermodel.SnapshotBackendCopy,
		Named:          &dockermodel.VolumePropertiesNamed{VolumeName: dockerName, UsedBy: usedBy},
	}
}

func bindVolume(service, target string) dockermodel.Volume {
	return dockermodel.Volume{
		ObjectID:       stackmodel.ServiceVolume{Type: dockermodel.VolumeTypeBind, Target: target}.ObjectID(service),
		ComposeProject: testComposeProject,
		Type:           dockermodel.VolumeTypeBind,
		Backend:        dockermodel.SnapshotBackendCopy,
		Bind:           &dockermodel.VolumePropertiesBind{Service: service, Target: target, Source: "/host" + target},
	}
}

// bindAddress is what the engine answers for a bindVolume: compose-go resolves
// a bind source to an absolute host path, so no lookup is involved.
func bindAddress(target string) dockermodel.MountSource {
	return dockermodel.MountSource{Type: dockermodel.MountSourceBind, Source: "/host" + target}
}
