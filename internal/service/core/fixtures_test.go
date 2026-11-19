// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// The provider's own refusals, which core passes through without matching.
var (
	errUnsupportedDaemon = errors.New("unsupported docker daemon")
	errLeftoverVolumes   = errors.New("compose project is not running but its volumes survive")
)

// The project every workflow test runs against: one service mounting one named
// volume.
// demoArcaveProject is the arcave project the deployment labels name.
const demoArcaveProject model.ArcaveProjectName = "demo-arcave"

const (
	demoProject = "demo"
	demoService = "db"
	demoTarget  = "/data"
	// demoVolumeSize is the size demoRecord's capture reported.
	demoVolumeSize = 4096
)

// demoRecord is a committed savepoint of demoVolume.
func demoRecord() model.Savepoint {
	return model.Savepoint{
		ID:        "019e843e-1650-7000-8000-000000000000",
		Name:      "sp",
		CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		Provider:  demoProviderName,
		Summary:   demoSavepointSummary(),
	}
}

// demoProviderName is the provider every committed fixture names; core reads nothing by it.
const demoProviderName = "fake"

// demoSavepointSummary is what the provider sums every committed fixture up as: demoVolume alone.
func demoSavepointSummary() model.SavepointSummary {
	return model.SavepointSummary{
		Units: []model.UnitTally{{Kind: model.UnitKindVolumes, Count: 1, SizeBytes: demoVolumeSize}},
	}
}

// demoSafetySnapshot is a committed safety snapshot of demoVolume: an ordinary
// record, newer than demoRecord, told apart by its label alone. It has no name: its field names
// the savepoint the rollback that took it was aiming at.
func demoSafetySnapshot() model.Savepoint {
	return model.Savepoint{
		ID:                    "019e844b-7818-7000-8000-000000000000",
		Labels:                safetySnapshotLabels(),
		TakenBeforeRollbackTo: &model.SavepointRef{ID: demoRecord().ID, Name: demoRecord().Name},
		CreatedAt:             time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		Provider:              demoProviderName,
		Summary:               demoSavepointSummary(),
	}
}

// newDirStore is a real directory store on a tempdir: the compose project's store a savepoint's
// heartbeat is written into.
func newDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// readSavepointHeartbeat reads the heartbeat at the root of savepoint id's tree.
func readSavepointHeartbeat(t *testing.T, store storages.Storager, id model.SavepointID) heartbeat.Heartbeat {
	t.Helper()
	tree, err := store.SubStorage(id.String(), true)
	require.NoError(t, err)
	beat, err := heartbeat.NewReader(tree).Read(context.Background())
	require.NoError(t, err)
	return beat
}

// expectLock declares a free lock and the release that always follows it —
// withUnlock defers the release, so a taken lock is a released one.
func expectLock(locker *mockLocker) *mock.Call {
	locker.On("Release").Return(nil).Once()
	return locker.On("Acquire", mock.Anything).Return(nil).Once()
}

// newSafetySnapshotRequest is the request a rollback to target captures its safety snapshot by:
// no name, the label, and target by the id and name it resolved to.
func newSafetySnapshotRequest(target model.Savepoint) provider.CaptureRequest {
	return provider.CaptureRequest{
		Labels: safetySnapshotLabels(),
		TakenBefore: &model.SavepointRef{
			ID:   target.ID,
			Name: target.Name,
		},
	}
}

// safetySnapshotLabels are the labels that mark a safety snapshot, spelled out rather than built
// by the code under test.
func safetySnapshotLabels() map[string]string {
	return map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
}

// ptrID and ptrBool render a literal as one of RollbackRequest's optional
// selectors; a composite literal cannot take the address of a constant.
func ptrID(id model.SavepointID) *model.SavepointID { return &id }

func ptrBool(b bool) *bool { return &b }

func ptrString(s string) *string { return &s }

// defaultSessionRef is the default session as the deployment labels name it.
var defaultSessionRef = model.SessionRef{
	ID:   model.DefaultSession,
	Name: model.DefaultSessionName,
}

func fixedNow() time.Time {
	return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
}

// assertLogOrder asserts log holds each of lines, in their order.
func assertLogOrder(t *testing.T, log string, lines []string) {
	t.Helper()
	at := -1
	for _, line := range lines {
		i := strings.Index(log, line)
		if !assert.Greater(t, i, at, "%q out of order or missing", line) {
			return
		}
		at = i
	}
}

// demoResolution is what every rollback by id opens with: the savepoints, listed twice, and a free
// lock.
func demoResolution(record model.Savepoint) (*mockSavepoints, *mockLocker) {
	savepoints := &mockSavepoints{}
	// Twice: once ahead of the locks, so a missing savepoint fails at once, and again under them.
	savepoints.On("List", mock.Anything).Return([]model.Savepoint{record}, nil).Twice()
	locker := &mockLocker{}
	expectLock(locker)
	return savepoints, locker
}

// emptySavepoints lists no savepoint, once.
func emptySavepoints() *mockSavepoints {
	savepoints := &mockSavepoints{}
	savepoints.On("List", mock.Anything).Return([]model.Savepoint(nil), nil).Once()
	return savepoints
}

// demoSummary is what building demoRecord restores.
func demoSummary() provider.PreparedSummary {
	return provider.PreparedSummary{
		Restored: model.RestoreSummary{Units: []model.UnitTally{{Kind: model.UnitKindVolumes, Count: 1}}},
		Detail:   json.RawMessage(`{"volumes":[{"object_id":"volume-named-demo_db"}]}`),
	}
}

// newDeploymentOf is the deployment core hands the provider for a build of savepoint in the
// default session.
func newDeploymentOf(savepoint model.Savepoint) model.SavepointDeployment {
	return model.SavepointDeployment{
		ArcaveProject: demoArcaveProject,
		Session:       defaultSessionRef,
		Savepoint: model.DeployedSavepoint{
			ID:               savepoint.ID,
			Name:             savepoint.Name,
			CreatedAt:        savepoint.CreatedAt,
			IsSafetySnapshot: savepoint.IsSafetySnapshot(),
		},
	}
}

// demoPrepareOptions are the options a rollback to savepoint prepares it by, recovering or not.
func demoPrepareOptions(savepoint model.Savepoint, recovering bool) provider.PrepareOptions {
	return provider.PrepareOptions{
		Deployment: newDeploymentOf(savepoint),
		Recovering: recovering,
	}
}
