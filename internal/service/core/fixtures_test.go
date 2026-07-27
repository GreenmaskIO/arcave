package core

import (
	"github.com/greenmaskio/arcave/internal/model"
)

// The project every workflow test runs against: one service mounting one named
// volume, spelled once per level each collaborator speaks at — facts in, unit
// through, stat out — so the three can never drift apart in a literal.
const (
	demoProject    = "demo"
	demoService    = "db"
	demoTarget     = "/data"
	demoVolumeName = "demo_db"
)

// demoIntrospection is what the compose inspector reports for that project.
func demoIntrospection() model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: demoService,
			Volumes: []model.ServiceVolume{{
				Type:       model.VolumeTypeNamed,
				RawType:    "volume",
				Source:     "db",
				Target:     demoTarget,
				VolumeName: demoVolumeName,
			}},
		}},
		Volumes: []model.NamedVolume{{
			Key:    "db",
			Name:   demoVolumeName,
			UsedBy: []model.ServiceReference{{Service: demoService, Target: demoTarget}},
		}},
	}}
}

// demoVolume is the unit FindBackupVolumes derives from it.
func demoVolume() model.Volume {
	return model.Volume{
		ObjectID: model.NamedVolume{Name: demoVolumeName}.ObjectID(),
		Type:     model.VolumeTypeNamed,
		Backend:  model.SnapshotBackendFullCopy,
		Named: &model.VolumePropertiesNamed{
			VolumeName: demoVolumeName,
			UsedBy:     []model.ServiceReference{{Service: demoService, Target: demoTarget}},
		},
	}
}

// demoStats is the bookkeeping a savepoint of it carries. No payload: nothing
// outside the backend reads one.
func demoStats() []model.SnapshotStat {
	return []model.SnapshotStat{{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}}
}
