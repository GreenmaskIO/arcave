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
	return model.Volume{ObjectID: "volume-named-" + demoVolumeName, Backend: model.SnapshotBackendCopy}
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

// demoSafetySnapshot is a committed safety snapshot of demoVolume: an ordinary
// record, newer than demoRecord, told apart by its label alone. Its name carries
// the ref the rollback that took it was aiming at.
func demoSafetySnapshot() model.Savepoint {
	return model.Savepoint{
		ID:        "1780335999",
		Name:      safetySnapshotName(demoRecord().ID),
		Labels:    safetySnapshotLabels(),
		CreatedAt: time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		Stats:     []model.SnapshotStat{{Volume: demoVolume(), Type: model.SnapshotBackendCopy, Payload: "safety-payload"}},
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
	expectLock(locker)
	return repo, inspector, locker
}

// expectLock declares a free lock and the release that always follows it —
// withUnlock defers the release, so a taken lock is a released one.
func expectLock(locker *mockLocker) *mock.Call {
	locker.On("Release").Return(nil).Once()
	return locker.On("Acquire", mock.Anything).Return(nil).Once()
}

// expectSafetySnapshotCommit declares the record a rollback commits for its undo:
// an id off the clock, the name carrying the id of the savepoint being restored,
// and the label that hides it from every scanning selector.
func expectSafetySnapshotCommit(repo *mockSavepointRepo, restoring model.SavepointID) *mock.Call {
	return repo.On("Save", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil && r.Name == "safety-snapshot-"+restoring.String() && r.IsSafetySnapshot()
	})).Return(nil).Once()
}

// expectSafetySnapshotUncommit declares the uncommit that opens the backup's drop.
// id is read at match time, so it may be filled by the capture that runs first.
func expectSafetySnapshotUncommit(repo *mockSavepointRepo, id *model.SavepointID) *mock.Call {
	return repo.On("Delete", mock.Anything, mock.MatchedBy(func(got model.SavepointID) bool {
		return got == *id
	})).Return(nil).Once()
}

// ptrID and ptrBool render a literal as one of RollbackRequest's optional
// selectors; a composite literal cannot take the address of a constant.
func ptrID(id model.SavepointID) *model.SavepointID { return &id }

func ptrBool(b bool) *bool { return &b }
