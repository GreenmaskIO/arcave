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

package garbage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/utils"
)

var (
	// noTrees matches the savepoint trees of a compose project store holding none.
	noTrees = mock.MatchedBy(func(savepoints []model.SavepointID) bool { return len(savepoints) == 0 })
	// deadSavepoint is a savepoint tree no record commits, whose capture is dead.
	deadSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000002")

	deadTreeOp = model.GarbageOp{
		Action: model.GarbageActionDeleteTree, Target: deadSavepoint.String(), Bytes: utils.New(int64(10)),
		Savepoint: deadSavepoint,
	}
	deadBlobOp = model.GarbageOp{
		Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/" + strings.Repeat("b", 64),
		Bytes: utils.New(int64(3)),
	}
	deadTagOp      = model.GarbageOp{Action: model.GarbageActionUntag, Target: "arcave/demo-db-1:sp-x"}
	deadSnapshotOp = model.GarbageOp{Action: model.GarbageActionForget, Target: "restic/volume-named-db:abc"}
	deadTreeStat   = model.GarbageStat(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadTreeOp}}})
	deadSnapStat   = model.GarbageStat(model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadSnapshotOp}}})
	deadTagStat    = model.GarbageStat(model.DockerLayerTagStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadTagOp}}})
)

// newMockDaemonCollector is a daemon collector resolving dataKind, named after it.
func newMockDaemonCollector(dataKind model.GarbageDataKind) *mockDaemonCollector {
	collector := &mockDaemonCollector{}
	collector.expectNames(dataKind)
	return collector
}

// newMockComposeProjectCollector is a compose-project collector resolving dataKind, named after it.
func newMockComposeProjectCollector(dataKind model.GarbageDataKind) *mockComposeProjectCollector {
	collector := &mockComposeProjectCollector{}
	collector.expectNames(dataKind)
	return collector
}

func (m *mockCollector) expectNames(dataKind model.GarbageDataKind) {
	m.On("Kind").Return(model.GarbageCollectorKind("mock-" + dataKind)).Maybe()
	m.On("DataKind").Return(dataKind).Maybe()
}

// newEmptyCollector is a compose-project collector of kind that finds nothing in compose project
// demo, as stat, once when it runs.
func newEmptyCollector(kind model.GarbageDataKind, stat model.GarbageStat, runs bool) *mockComposeProjectCollector {
	collector := newMockComposeProjectCollector(kind)
	if runs {
		collector.On("Find", mock.Anything, inComposeProject("demo")).Return(stat, nil).Once()
	}
	return collector
}

// newMockEraser is an eraser carrying out action.
func newMockEraser(action model.GarbageAction) *mockEraser {
	eraser := &mockEraser{}
	eraser.On("Action").Return(action).Maybe()
	return eraser
}

// newMockDaemonEraser is a daemon eraser carrying out action.
func newMockDaemonEraser(action model.GarbageAction) *mockDaemonEraser {
	eraser := &mockDaemonEraser{}
	eraser.On("Action").Return(action).Maybe()
	return eraser
}

// newDeletedOps is a deletion of every op of ops.
func newDeletedOps(ops ...model.GarbageOp) model.GarbageDeletion {
	var deleted []model.GarbageDeleted
	for _, op := range ops {
		deleted = append(deleted, model.GarbageDeleted{Target: op.Target, Bytes: op.Bytes})
	}
	return model.GarbageDeletion{Deleted: deleted}
}

// finderDeps are a finder's deps beside the store its default session is found in.
type finderDeps struct {
	Deps
	store storages.Navigator
}

// newFinderDeps is a finder over store's default session; the collectors and the savepoint readers
// are the test's.
func newFinderDeps(store storages.Navigator) finderDeps {
	return finderDeps{store: store}
}

// newSessionStore is an arcave project store holding, in the default session, the compose projects
// names.
func newSessionStore(t *testing.T, names ...model.DockerComposeProjectName) storages.Storager {
	t.Helper()
	store := inmemory.New("")
	for _, name := range names {
		key := "sessions/" + model.DefaultSession.String() + "/compose/" + name.String() + "/.keep"
		require.NoError(t, store.PutObject(t.Context(), key, strings.NewReader("")))
	}
	return store
}

// unlistableStore refuses every directory listing, as a store whose credentials stop at reading.
type unlistableStore struct{ storages.Storager }

func (s *unlistableStore) SubStorage(subPath string, relative bool) (storages.Storager, error) {
	sub, err := s.Storager.SubStorage(subPath, relative)
	if err != nil {
		return nil, err
	}
	return &unlistableStore{Storager: sub}, nil
}

func (s *unlistableStore) ListDir(context.Context) ([]string, []storages.Storager, error) {
	return nil, nil, errors.New("access denied")
}

// newSavepointsInOrder opens repos in turn, one per compose project the finder reads.
func newSavepointsInOrder(t *testing.T, repos ...*mockSavepointReader) func(storages.Storager) SavepointReader {
	next := 0
	return func(storages.Storager) SavepointReader {
		require.Less(t, next, len(repos), "a compose project read past the repositories given")
		repo := repos[next]
		next++
		return repo
	}
}

// newEmptySavepoints is a compose project's savepoints, none committed and no tree, read once and
// asserted when the test ends.
func newEmptySavepoints(t *testing.T) *mockSavepointReader {
	savepoints := &mockSavepointReader{}
	savepoints.On("LoadSavepoints", mock.Anything, noTrees).Return([]model.Savepoint(nil), nil).Once()
	t.Cleanup(func() { savepoints.AssertExpectations(t) })
	return savepoints
}

func newDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// inComposeProject matches the scope of compose project name.
func inComposeProject(name model.DockerComposeProjectName) any {
	return mock.MatchedBy(func(scope ComposeProjectScope) bool { return scope.ComposeProject == name })
}

// forComposeProjects matches the daemon scope of the session's compose projects names.
func forComposeProjects(names ...model.DockerComposeProjectName) any {
	return mock.MatchedBy(func(scope DaemonScope) bool { return slices.Equal(scope.ComposeProjects, names) })
}

// storeOf matches the store of compose project name.
func storeOf(name model.DockerComposeProjectName) any {
	return mock.MatchedBy(func(store storages.Navigator) bool { return store.Dirname() == string(name) })
}

// reportsOf are the reports of found, in order.
func reportsOf(found []provider.Garbage) []model.GarbageReport {
	reports := make([]model.GarbageReport, 0, len(found))
	for _, garbage := range found {
		reports = append(reports, garbage.Report())
	}
	return reports
}

// The daemon's reports come first, then each compose project's in the collectors' order, each over
// its compose project's scope: its store, listing and records.
func TestFinder_Find(t *testing.T) {
	// Arrange
	savepoints := []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000001"}}
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, noTrees).Return(savepoints, nil).Once()
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	blobStat := model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadBlobOp}}}
	blobs.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool {
		opened := scope.Store != nil
		scope.Store, scope.Objects = nil, nil
		return opened && assert.ObjectsAreEqual(ComposeProjectScope{
			ComposeProject: "demo", Savepoints: savepoints,
		}, scope)
	})).Return(blobStat, nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	trees.On("Find", mock.Anything, inComposeProject("demo")).Return(deadTreeStat, nil).Once()
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	tags.On("Find", mock.Anything, forComposeProjects("demo")).Return(deadTagStat, nil).Once()
	deps := newFinderDeps(newSessionStore(t, "demo"))
	deps.NewSavepointReader = newSavepointsInOrder(t, demoSavepoints)
	deps.DaemonCollectors = []DaemonCollector{tags}
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs, trees}

	// Act
	found := New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, nil)

	// Assert
	assert.Equal(t, []model.GarbageReport{
		{
			DataKind:  model.GarbageDataKindRestoredLayers,
			Collector: "mock-restored-layers",
			Summary:   deadTagStat.Summary(),
			Detail:    deadTagStat,
		},
		{
			DataKind:       model.GarbageDataKindImageLayers,
			Collector:      "mock-image-layers",
			ComposeProject: "demo",
			Summary:        blobStat.Summary(),
			Detail:         blobStat,
		},
		{
			DataKind:       model.GarbageDataKindSavepointData,
			Collector:      "mock-savepoint-data",
			ComposeProject: "demo",
			Summary:        deadTreeStat.Summary(),
			Detail:         deadTreeStat,
		},
	}, reportsOf(found))
	demoSavepoints.AssertExpectations(t)
	blobs.AssertExpectations(t)
	trees.AssertExpectations(t)
	tags.AssertExpectations(t)
}

func TestFinder_Find_FailuresStayInTheirReport(t *testing.T) {
	// Arrange
	brokenSavepoints := &mockSavepointReader{}
	brokenSavepoints.On("LoadSavepoints", mock.Anything, noTrees).
		Return([]model.Savepoint(nil), errors.New("corrupt record")).Once()
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	blobs.On("Find", mock.Anything, inComposeProject("demo")).Return(nil, errors.New("listing failed")).Once()
	images := newMockComposeProjectCollector(model.GarbageDataKindUpstreamImages)
	images.On("Find", mock.Anything, inComposeProject("demo")).
		Return(model.OCIImageStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
	deps := newFinderDeps(newSessionStore(t, "broken", "demo"))
	deps.NewSavepointReader = newSavepointsInOrder(t, brokenSavepoints, newEmptySavepoints(t))
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs, images}

	// Act
	reports := reportsOf(New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, nil))

	// Assert
	require.Len(t, reports, 4)
	// the first compose project's mark could not be read: every compose kind is aborted, none runs
	assert.Contains(t, reports[0].Summary.Aborted, "corrupt record")
	assert.Contains(t, reports[1].Summary.Aborted, "corrupt record")
	// the second's blob collector failed; the image collector after it still ran
	assert.Equal(t, "listing failed", reports[2].Error)
	assert.Equal(t, model.GarbageDataKindUpstreamImages, reports[3].DataKind)
	assert.Empty(t, reports[3].Error)
	brokenSavepoints.AssertExpectations(t)
	blobs.AssertExpectations(t)
	images.AssertExpectations(t)
}

// A session whose compose projects cannot be listed gives the daemon's objects nothing to be
// judged against: the daemon kinds abort with the compose ones.
func TestFinder_Find_UnlistedSessionAbortsTheDaemon(t *testing.T) {
	// Arrange: neither collector expects Find, so one would fail the mock.
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
	deps := newFinderDeps(&unlistableStore{Storager: inmemory.New("")})
	deps.DaemonCollectors = []DaemonCollector{tags}
	deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs}

	// Act
	reports := reportsOf(New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, nil))

	// Assert
	require.Len(t, reports, 2)
	assert.Equal(t, model.GarbageDataKindRestoredLayers, reports[0].DataKind)
	assert.Contains(t, reports[0].Summary.Aborted, "access denied")
	assert.Contains(t, reports[1].Summary.Aborted, "access denied")
	tags.AssertExpectations(t)
	blobs.AssertExpectations(t)
}

// A collector that returns neither a finding nor an error fails its own report.
func TestFinder_Find_NoFinding(t *testing.T) {
	// Arrange
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	tags.On("Find", mock.Anything, forComposeProjects()).Return(nil, nil).Once()
	deps := newFinderDeps(inmemory.New(""))
	deps.DaemonCollectors = []DaemonCollector{tags}

	// Act
	reports := reportsOf(New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, nil))

	// Assert
	require.Len(t, reports, 1)
	assert.Contains(t, reports[0].Error, "no finding")
	tags.AssertExpectations(t)
}

// Every tree no record commits is dead whatever its heartbeat says.
func TestFinder_Find_UncommittedTreesAreDead(t *testing.T) {
	const tree = model.SavepointID("019e843e-1650-7000-8000-000000000003")

	tests := []struct {
		name string
		// raw is written into the tree as its heartbeat when not empty.
		raw       string
		committed bool
		wantDead  []model.SavepointID
	}{
		{
			name:     "a tree without a heartbeat is dead",
			wantDead: []model.SavepointID{tree},
		},
		{
			name:     "a fresh in-progress heartbeat is dead",
			raw:      `{"operation":"savepoint","status":"in-progress"}`,
			wantDead: []model.SavepointID{tree},
		},
		{
			name:     "an unreadable heartbeat is dead",
			raw:      "{torn",
			wantDead: []model.SavepointID{tree},
		},
		{
			name:      "a record commits its tree",
			raw:       `{"operation":"savepoint","status":"in-progress"}`,
			committed: true,
			wantDead:  []model.SavepointID{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()
			store := newDirStore(t)
			composeProjectStore, err := store.SubStorage("sessions/"+model.DefaultSession.String()+"/compose/demo", true)
			require.NoError(t, err)
			require.NoError(t, composeProjectStore.PutObject(ctx, tree.String()+"/volume-named-db/data.tar", strings.NewReader("x")))
			if tt.raw != "" {
				require.NoError(t, composeProjectStore.PutObject(ctx, tree.String()+"/"+heartbeat.FileName, strings.NewReader(tt.raw)))
			}
			var savepoints []model.Savepoint
			if tt.committed {
				savepoints = []model.Savepoint{{ID: tree}}
			}
			demoSavepoints := &mockSavepointReader{}
			demoSavepoints.On("LoadSavepoints", mock.Anything, []model.SavepointID{tree}).Return(savepoints, nil).Once()
			trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
			trees.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool {
				return scope.ComposeProject == "demo" && assert.ObjectsAreEqual(tt.wantDead, scope.Dead)
			})).Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			deps := newFinderDeps(store)
			deps.NewSavepointReader = newSavepointsInOrder(t, demoSavepoints)
			deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}

			// Act
			New(deps.Deps).Find(ctx, deps.store, model.DefaultSession, nil)

			// Assert
			demoSavepoints.AssertExpectations(t)
			trees.AssertExpectations(t)
		})
	}
}

// The data kinds pick the collectors. No compose-project collector, no scope read.
func TestFinder_Find_SelectsDataKinds(t *testing.T) {
	tests := []struct {
		name      string
		dataKinds []model.GarbageDataKind
		wantKinds []model.GarbageDataKind
		// readsScopes is whether the compose projects' scopes are read.
		readsScopes bool
	}{
		{
			name: "every data kind by default",
			wantKinds: []model.GarbageDataKind{
				model.GarbageDataKindRestoredLayers, model.GarbageDataKindSavepointData, model.GarbageDataKindUpstreamImages,
				model.GarbageDataKindImageLayers, model.GarbageDataKindDeduplicatedVolumes,
			},
			readsScopes: true,
		},
		{
			name:      "restored layers alone",
			dataKinds: []model.GarbageDataKind{model.GarbageDataKindRestoredLayers},
			wantKinds: []model.GarbageDataKind{model.GarbageDataKindRestoredLayers},
		},
		{
			name: "two data kinds, in the collectors' order",
			dataKinds: []model.GarbageDataKind{
				model.GarbageDataKindDeduplicatedVolumes, model.GarbageDataKindSavepointData,
			},
			wantKinds: []model.GarbageDataKind{
				model.GarbageDataKindSavepointData, model.GarbageDataKindDeduplicatedVolumes,
			},
			readsScopes: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var readers []*mockSavepointReader
			if tt.readsScopes {
				readers = append(readers, newEmptySavepoints(t))
			}
			runs := func(kind model.GarbageDataKind) bool { return slices.Contains(tt.wantKinds, kind) }
			tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
			if runs(model.GarbageDataKindRestoredLayers) {
				tags.On("Find", mock.Anything, forComposeProjects("demo")).
					Return(model.DockerLayerTagStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			}
			composeProjectCollectors := []*mockComposeProjectCollector{
				newEmptyCollector(model.GarbageDataKindSavepointData, model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
					runs(model.GarbageDataKindSavepointData)),
				newEmptyCollector(model.GarbageDataKindUpstreamImages, model.OCIImageStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
					runs(model.GarbageDataKindUpstreamImages)),
				newEmptyCollector(model.GarbageDataKindImageLayers, model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
					runs(model.GarbageDataKindImageLayers)),
				newEmptyCollector(model.GarbageDataKindDeduplicatedVolumes, model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
					runs(model.GarbageDataKindDeduplicatedVolumes)),
			}
			deps := newFinderDeps(newSessionStore(t, "demo"))
			deps.NewSavepointReader = newSavepointsInOrder(t, readers...)
			deps.DaemonCollectors = []DaemonCollector{tags}
			for _, collector := range composeProjectCollectors {
				deps.ComposeProjectCollectors = append(deps.ComposeProjectCollectors, collector)
			}

			// Act
			reports := reportsOf(New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, tt.dataKinds))

			// Assert
			var kinds []model.GarbageDataKind
			for _, report := range reports {
				kinds = append(kinds, report.DataKind)
			}
			assert.Equal(t, tt.wantKinds, kinds, "the daemon collectors run first, then the compose-project ones in their order")
			tags.AssertExpectations(t)
			for _, collector := range composeProjectCollectors {
				collector.AssertExpectations(t)
			}
		})
	}
}

// A collector that cannot reach what it reads is aborted, at either level and whether or not the
// data kinds named it: a report that looked at nothing would read as clean.
func TestFinder_Find_ProbeFails(t *testing.T) {
	tests := []struct {
		name      string
		dataKinds []model.GarbageDataKind
	}{
		{
			name: "no data kind named",
		},
		{
			name: "the data kinds named",
			dataKinds: []model.GarbageDataKind{
				model.GarbageDataKindRestoredLayers, model.GarbageDataKindImageLayers, model.GarbageDataKindDeduplicatedVolumes,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			demoSavepoints := newEmptySavepoints(t)
			tags := &mockProbeCollector{}
			tags.On("Kind").Return(model.GarbageCollectorKindDockerLayerTags).Maybe()
			tags.On("DataKind").Return(model.GarbageDataKindRestoredLayers).Maybe()
			tags.On("Probe", mock.Anything).Return(errors.New("connection refused")).Once()
			restic := &mockComposeProjectProbeCollector{}
			restic.On("Kind").Return(model.GarbageCollectorKindResticRepositories).Maybe()
			restic.On("DataKind").Return(model.GarbageDataKindDeduplicatedVolumes).Maybe()
			restic.On("Probe", mock.Anything).Return(errors.New("connection refused")).Once()
			blobs := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			blobs.On("Find", mock.Anything, inComposeProject("demo")).
				Return(model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
			deps := newFinderDeps(newSessionStore(t, "demo"))
			deps.NewSavepointReader = newSavepointsInOrder(t, demoSavepoints)
			deps.DaemonCollectors = []DaemonCollector{tags}
			deps.ComposeProjectCollectors = []ComposeProjectCollector{blobs, restic}

			// Act
			reports := reportsOf(New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, tt.dataKinds))

			// Assert
			aborted := map[model.GarbageDataKind]string{}
			var kinds []model.GarbageDataKind
			for _, report := range reports {
				kinds = append(kinds, report.DataKind)
				aborted[report.DataKind] = report.Summary.Aborted
			}
			assert.Equal(t, []model.GarbageDataKind{
				model.GarbageDataKindRestoredLayers, model.GarbageDataKindImageLayers, model.GarbageDataKindDeduplicatedVolumes,
			}, kinds)
			assert.Equal(t, map[model.GarbageDataKind]string{
				model.GarbageDataKindRestoredLayers:      "connection refused",
				model.GarbageDataKindImageLayers:         "",
				model.GarbageDataKindDeduplicatedVolumes: "connection refused",
			}, aborted)
			tags.AssertExpectations(t)
			restic.AssertExpectations(t)
			blobs.AssertExpectations(t)
			demoSavepoints.AssertExpectations(t)
		})
	}
}

// The scope lists every object of the compose project store, by its key from the store's root,
// except under an unlisted namespace.
func TestFinder_Find_ListsAllButTheUnlistedNamespaces(t *testing.T) {
	// Arrange
	ctx := context.Background()
	const tree = model.SavepointID("019e843e-1650-7000-8000-000000000003")
	store := newDirStore(t)
	composeProjectStore, err := store.SubStorage("sessions/"+model.DefaultSession.String()+"/compose/demo", true)
	require.NoError(t, err)
	for _, key := range []string{
		"root.keep",
		tree.String() + "/volume-named-db/data.tar",
		"shared/oci/blobs/sha256/" + strings.Repeat("a", 64),
		"restic/volume-named-db/config",
		"restic/volume-named-db/data/00/" + strings.Repeat("0", 64),
	} {
		require.NoError(t, composeProjectStore.PutObject(ctx, key, strings.NewReader("x")))
	}
	wantNames := []string{
		tree.String() + "/volume-named-db/data.tar",
		"root.keep",
		"shared/oci/blobs/sha256/" + strings.Repeat("a", 64),
	}
	demoSavepoints := &mockSavepointReader{}
	demoSavepoints.On("LoadSavepoints", mock.Anything, []model.SavepointID{tree}).Return([]model.Savepoint(nil), nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	trees.On("Find", mock.Anything, mock.MatchedBy(func(scope ComposeProjectScope) bool {
		var names []string
		for _, object := range scope.Objects {
			if object.Size != 1 || object.LastModified.IsZero() {
				return false
			}
			names = append(names, object.Name)
		}
		return slices.Equal(wantNames, names)
	})).Return(model.SavepointDirectoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil).Once()
	deps := newFinderDeps(store)
	deps.NewSavepointReader = newSavepointsInOrder(t, demoSavepoints)
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees}
	deps.UnlistedNamespaces = []string{"restic"}

	// Act
	New(deps.Deps).Find(ctx, deps.store, model.DefaultSession, nil)

	// Assert
	demoSavepoints.AssertExpectations(t)
	trees.AssertExpectations(t)
}

// Each finding is deleted by the eraser of its action at its level, a compose project's in that
// compose project's store; what a prune reclaims rides on the deletion.
func TestFound_Delete(t *testing.T) {
	// Arrange
	var (
		tagDeleted = model.GarbageDeletion{Deleted: []model.GarbageDeleted{{Target: deadTagOp.Target, Bytes: utils.New(int64(0))}}}
		forgotten  = model.GarbageDeletion{
			Deleted:   []model.GarbageDeleted{{Target: deadSnapshotOp.Target, Bytes: utils.New(int64(0))}},
			Reclaimed: []model.GarbageReclaimed{{Target: "restic/volume-named-db", Bytes: utils.New(int64(5))}},
		}
	)
	tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
	tags.On("Find", mock.Anything, forComposeProjects("demo", "other")).Return(deadTagStat, nil).Once()
	trees := newMockComposeProjectCollector(model.GarbageDataKindSavepointData)
	restic := newMockComposeProjectCollector(model.GarbageDataKindDeduplicatedVolumes)
	treeEraser := newMockEraser(model.GarbageActionDeleteTree)
	forgetEraser := newMockEraser(model.GarbageActionForget)
	tagEraser := newMockDaemonEraser(model.GarbageActionUntag)
	tagEraser.On("Erase", mock.Anything, []model.GarbageOp{deadTagOp}).Return(tagDeleted).Once()
	for _, name := range []model.DockerComposeProjectName{"demo", "other"} {
		trees.On("Find", mock.Anything, inComposeProject(name)).Return(deadTreeStat, nil).Once()
		restic.On("Find", mock.Anything, inComposeProject(name)).Return(deadSnapStat, nil).Once()
		treeEraser.On("Erase", mock.Anything, storeOf(name), []model.GarbageOp{deadTreeOp}).Return(newDeletedOps(deadTreeOp)).Once()
		forgetEraser.On("Erase", mock.Anything, storeOf(name), []model.GarbageOp{deadSnapshotOp}).Return(forgotten).Once()
	}
	deps := newFinderDeps(newSessionStore(t, "demo", "other"))
	deps.NewSavepointReader = newSavepointsInOrder(t, newEmptySavepoints(t), newEmptySavepoints(t))
	deps.DaemonCollectors = []DaemonCollector{tags}
	deps.ComposeProjectCollectors = []ComposeProjectCollector{trees, restic}
	deps.DaemonErasers = []DaemonEraser{tagEraser}
	deps.ComposeProjectErasers = []ComposeProjectEraser{treeEraser, newMockEraser(model.GarbageActionDeleteImage), forgetEraser}
	found := New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, nil)

	// Act
	deletions := make([]model.GarbageDeletion, 0, len(found))
	for _, garbage := range found {
		deletions = append(deletions, garbage.Delete(context.Background()))
	}

	// Assert
	assert.Equal(t, []model.GarbageDeletion{
		tagDeleted,
		newDeletedOps(deadTreeOp), forgotten,
		newDeletedOps(deadTreeOp), forgotten,
	}, deletions)
	tags.AssertExpectations(t)
	trees.AssertExpectations(t)
	restic.AssertExpectations(t)
	treeEraser.AssertExpectations(t)
	forgetEraser.AssertExpectations(t)
	tagEraser.AssertExpectations(t)
}

// A finding with no op deletes nothing; one whose ops no one eraser carries out — an action none
// has, or several actions — fails every op, at either level.
func TestFound_Delete_NothingToErase(t *testing.T) {
	tests := []struct {
		name string
		// daemon finds stat at the daemon's level, else in compose project demo.
		daemon bool
		// daemonAction is what the one daemon eraser carries out.
		daemonAction model.GarbageAction
		stat         model.GarbageStat
		want         model.GarbageDeletion
	}{
		{
			name: "no op",
			stat: model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}},
			want: model.GarbageDeletion{},
		},
		{
			name: "an action no eraser carries out",
			stat: deadSnapStat,
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{
				{Target: deadSnapshotOp.Target, Error: errNoEraser.Error()},
			}},
		},
		{
			name: "ops of several actions",
			stat: model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadBlobOp, deadTreeOp}}},
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{
				{Target: deadBlobOp.Target, Error: errNoEraser.Error()},
				{Target: deadTreeOp.Target, Error: errNoEraser.Error()},
			}},
		},
		{
			name:         "an action no daemon eraser carries out",
			daemon:       true,
			daemonAction: model.GarbageActionDeleteTree,
			stat:         deadTagStat,
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{
				{Target: deadTagOp.Target, Error: errNoEraser.Error()},
			}},
		},
		{
			name:         "daemon ops of several actions",
			daemon:       true,
			daemonAction: model.GarbageActionUntag,
			stat:         model.DockerLayerTagStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{deadTagOp, deadTreeOp}}},
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{
				{Target: deadTagOp.Target, Error: errNoEraser.Error()},
				{Target: deadTreeOp.Target, Error: errNoEraser.Error()},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: no eraser expects Erase, so one would fail the mock.
			objectEraser := newMockEraser(model.GarbageActionDeleteObject)
			treeEraser := newMockEraser(model.GarbageActionDeleteTree)
			daemonEraser := newMockDaemonEraser(tt.daemonAction)
			tags := newMockDaemonCollector(model.GarbageDataKindRestoredLayers)
			collector := newMockComposeProjectCollector(model.GarbageDataKindImageLayers)
			deps := newFinderDeps(newSessionStore(t, "demo"))
			deps.ComposeProjectErasers = []ComposeProjectEraser{objectEraser, treeEraser}
			deps.DaemonErasers = []DaemonEraser{daemonEraser}
			if tt.daemon {
				tags.On("Find", mock.Anything, forComposeProjects("demo")).Return(tt.stat, nil).Once()
				deps.DaemonCollectors = []DaemonCollector{tags}
			} else {
				collector.On("Find", mock.Anything, inComposeProject("demo")).Return(tt.stat, nil).Once()
				deps.NewSavepointReader = newSavepointsInOrder(t, newEmptySavepoints(t))
				deps.ComposeProjectCollectors = []ComposeProjectCollector{collector}
			}
			found := New(deps.Deps).Find(context.Background(), deps.store, model.DefaultSession, nil)
			require.Len(t, found, 1)

			// Act
			got := found[0].Delete(context.Background())

			// Assert
			assert.Equal(t, tt.want, got)
			tags.AssertExpectations(t)
			collector.AssertExpectations(t)
			objectEraser.AssertExpectations(t)
			treeEraser.AssertExpectations(t)
			daemonEraser.AssertExpectations(t)
		})
	}
}
