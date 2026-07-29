package core

import (
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// The project every workflow test runs against: one service mounting one named
// volume.
const (
	demoProject    = "demo"
	demoService    = "db"
	demoTarget     = "/data"
	demoVolumeName = "demo_db"
)

// demoComposeFiles is the file set every workflow request names.
func demoComposeFiles() []string { return []string{"docker-compose.yml"} }

// demoVolume is the unit, in the form the mock tests address it by.
func demoVolume() model.Volume {
	return model.Volume{ObjectID: "volume/" + demoVolumeName, Backend: model.SnapshotBackendCopy}
}

// demoRecord is a committed savepoint of demoVolume.
func demoRecord() model.Savepoint {
	return model.Savepoint{
		ID:        "1780335122",
		Name:      "sp",
		CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		Stats:     []model.SnapshotStat{{Volume: demoVolume(), Type: model.SnapshotBackendCopy, Payload: "sp-payload"}},
	}
}

// demoIntrospection is what the compose inspector reports for the project.
func demoIntrospection() model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Volumes: []model.NamedVolume{{Key: "db", Name: demoVolumeName,
			UsedBy: []model.ServiceReference{{Service: demoService, Target: demoTarget}}}},
	}}
}

// demoResolution is the three reads every rollback opens with: the records, the
// introspection, and a free lock.
func demoResolution(record model.Savepoint) (*mockSavepointRepo, *mockInspector, *mockLocker) {
	repo := &mockSavepointRepo{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	locker.On("Acquire", mock.Anything).Return(nil)
	return repo, inspector, locker
}
