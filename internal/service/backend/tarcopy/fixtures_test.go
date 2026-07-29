package tarcopy

import (
	"github.com/greenmaskio/arcave/internal/model"
)

const testComposeProject = "demo"

func namedVolume(dockerName string, usedBy ...model.ServiceReference) model.Volume {
	return model.Volume{
		ObjectID:       model.NamedVolume{Name: dockerName}.ObjectID(),
		ComposeProject: testComposeProject,
		Type:           model.VolumeTypeNamed,
		Backend:        model.SnapshotBackendCopy,
		Named:          &model.VolumePropertiesNamed{VolumeName: dockerName, UsedBy: usedBy},
	}
}

func bindVolume(service, target string) model.Volume {
	return model.Volume{
		ObjectID:       model.ServiceVolume{Type: model.VolumeTypeBind, Target: target}.ObjectID(service),
		ComposeProject: testComposeProject,
		Type:           model.VolumeTypeBind,
		Backend:        model.SnapshotBackendCopy,
		Bind:           &model.VolumePropertiesBind{Service: service, Target: target},
	}
}

func anonVolume(service, target string) model.Volume {
	return model.Volume{
		ObjectID:       model.ServiceVolume{Type: model.VolumeTypeAnon, Target: target}.ObjectID(service),
		ComposeProject: testComposeProject,
		Type:           model.VolumeTypeAnon,
		Backend:        model.SnapshotBackendCopy,
		Anon:           &model.VolumePropertiesAnon{Service: service, Target: target},
	}
}
