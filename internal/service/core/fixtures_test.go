package core

import (
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/compose-spec/compose-go/v2/types"
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
		Spec:      demoSpecRef(),
	}
}

// demoSpecRef names the artifact every committed fixture carries, digest included.
func demoSpecRef() *model.SpecRef {
	return &model.SpecRef{Format: model.SpecFormatComposeYAML}
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
		Spec:      demoSpecRef(),
	}
}

// demoIntrospection is what the inspector reports for the live stack.
func demoIntrospection() model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Volumes: []model.NamedVolume{{Key: "db", Name: demoVolumeName,
			UsedBy: []model.ServiceReference{{Service: demoService, Target: demoTarget}}}},
	}}
}

// The one replica demoIntrospectionWithContainers reports for demoService — the
// unit an image capture commits.
const (
	demoContainerID = "c0ffee"
	demoBaseImage   = "postgres:16"
)

// demoContainerRef is that replica in the form a capture request addresses it by.
func demoContainerRef() model.ContainerRef {
	return model.ContainerRef{
		Service: demoService, Replica: 1, ID: demoContainerID, BaseImage: demoBaseImage,
	}
}

// demoIntrospectionWithContainers is demoIntrospection with the daemon holding
// one container for demoService: what a workflow that captures layers sees.
func demoIntrospectionWithContainers() model.IntrospectionResult {
	res := demoIntrospection()
	res.Project.Services = []model.Service{{
		Name: demoService,
		Containers: []model.Container{{
			ID:      demoContainerID,
			Number:  1,
			Inspect: container.InspectResponse{Config: &container.Config{Image: demoBaseImage}},
		}},
	}}
	return res
}

// demoImageCapture is what the copier reports for demoContainerRef.
func demoImageCapture() model.ImageCapture {
	return model.ImageCapture{
		Service:     demoService,
		Replica:     1,
		ContainerID: demoContainerID,
		ImageID:     "sha256:aaaa",
		BaseImage:   demoBaseImage,
		SizeBytes:   4096,
	}
}

// demoRecordWithImages is demoRecord carrying the writable layer of its one replica.
func demoRecordWithImages() model.Savepoint {
	record := demoRecord()
	record.Images = []model.ImageCapture{demoImageCapture()}
	return record
}

// demoRestoredRef is the tag a rollback of demoRecord puts on one replica's layer;
// demoBaseImage's colon is what a docker repository path cannot carry.
func demoRestoredRef(replica int) string {
	return fmt.Sprintf("arcave/%s-%s-%d-postgres-16:sp-%s", demoProject, demoService, replica, demoRecord().ID)
}

// demoImageRestore is what the copier reports for demoImageCapture.
func demoImageRestore() model.ImageRestore {
	return model.ImageRestore{
		Service: demoService, Replica: 1,
		ImageID: demoImageCapture().ImageID, ImageRef: demoRestoredRef(1),
	}
}

// The second replica of demoService, and the layer it carries of its own — what a
// compose service's one image cannot stand for.
const (
	demoSecondContainerID = "cafe"
	demoSecondImageID     = "sha256:bbbb"
)

// demoScaledIntrospection is demoIntrospectionWithContainers with demoService
// scaled to two replicas.
func demoScaledIntrospection() model.IntrospectionResult {
	res := demoIntrospectionWithContainers()
	res.Project.Services[0].Containers = append(res.Project.Services[0].Containers, model.Container{
		ID:      demoSecondContainerID,
		Number:  2,
		Inspect: container.InspectResponse{Config: &container.Config{Image: demoBaseImage}},
	})
	return res
}

// demoSecondImageCapture is the second replica's own layer.
func demoSecondImageCapture() model.ImageCapture {
	capture := demoImageCapture()
	capture.Replica = 2
	capture.ContainerID = demoSecondContainerID
	capture.ImageID = demoSecondImageID
	return capture
}

// demoScaledRecord is a savepoint of the scaled stack: one capture per replica.
func demoScaledRecord() model.Savepoint {
	record := demoRecord()
	record.Images = []model.ImageCapture{demoImageCapture(), demoSecondImageCapture()}
	return record
}

// demoSpec is what the assembler materialises demoIntrospection into, demoSpecBytes
// its artifact, and demoTargetSpec what that artifact parses back to. The two
// projects are distinct values: expectations match them by identity, so a call
// handed the live stack instead of the savepoint's fails.
var (
	demoSpec       = &types.Project{Name: demoProject}
	demoTargetSpec = &types.Project{Name: demoProject, Services: types.Services{
		demoService: {Name: demoService, Image: demoBaseImage},
	}}
	demoSpecBytes = []byte("name: demo\n")
)

// demoAssembler materialises demoIntrospection into demoSpec.
func demoAssembler() *mockAssembler { return assemblerFor(demoIntrospection()) }

// assemblerFor is demoAssembler for a test that reports its own introspection.
func assemblerFor(res model.IntrospectionResult) *mockAssembler {
	assembler := &mockAssembler{}
	assembler.On("Assemble", res).Return(demoSpec, nil)
	assembler.On("Marshal", demoSpec).Return(demoSpecBytes, nil)
	assembler.On("Parse", mock.Anything, demoSpecBytes).Return(demoTargetSpec, nil)
	return assembler
}

// demoResolution is the three reads every rollback opens with: the records, the
// introspection, and a free lock.
func demoResolution(record model.Savepoint) (*mockSavepointRepo, *mockInspector, *mockLocker) {
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("LoadSpec", mock.Anything, record.ID).Return(demoSpecBytes, nil)
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
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

// expectSpecCommit declares the artifact a capture writes before its record. The
// id comes off the clock, so only the payload can be named.
func expectSpecCommit(repo *mockSavepointRepo) *mock.Call {
	return repo.On("SaveSpec", mock.Anything, mock.Anything, demoSpecBytes).Return(nil).Once()
}

// expectSafetySnapshotCommit declares the record a rollback commits for its undo:
// an id off the clock, the name carrying the id of the savepoint being restored,
// and the label that hides it from every scanning selector.
func expectSafetySnapshotCommit(repo *mockSavepointRepo, restoring model.SavepointID) *mock.Call {
	return repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil && r.Name == "safety-snapshot-"+restoring.String() &&
			r.IsSafetySnapshot() && r.Spec != nil
	})).Return(nil).Once()
}

// expectSafetySnapshotUncommit declares the uncommit that opens the backup's drop.
// id is read at match time, so it may be filled by the capture that runs first.
func expectSafetySnapshotUncommit(repo *mockSavepointRepo, id *model.SavepointID) *mock.Call {
	return repo.On("DeleteSavepoint", mock.Anything, mock.MatchedBy(func(got model.SavepointID) bool {
		return got == *id
	})).Return(nil).Once()
}

// ptrID and ptrBool render a literal as one of RollbackRequest's optional
// selectors; a composite literal cannot take the address of a constant.
func ptrID(id model.SavepointID) *model.SavepointID { return &id }

func ptrBool(b bool) *bool { return &b }
