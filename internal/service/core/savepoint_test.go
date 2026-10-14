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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	savepointrepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
)

func TestService_CreateSavepoint(t *testing.T) {
	// Arrange
	// One named volume and one bind mount are units; the tmpfs mount is a skip and
	// web, mounting nothing, is not covered.
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{
				Name: "app",
				Volumes: []model.ServiceVolume{
					{Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg"},
					{Type: model.VolumeTypeTmpfs, RawType: "tmpfs", Target: "/scratch"},
				},
			},
			{
				Name: "db",
				Volumes: []model.ServiceVolume{{
					Type: model.VolumeTypeNamed, RawType: "volume", Source: "pgdata",
					Target: "/var/lib/postgresql/data", VolumeName: "demo_pgdata",
				}},
			},
			{Name: "web"},
		},
		Volumes: []model.NamedVolume{{
			Key: "pgdata", Name: "demo_pgdata",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		}},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return([]string{"c0ffee"}, nil).Once()
	wantNotCovered := &model.NotCovered{
		Mounts: []model.SkippedVolume{{Service: "app", Target: "/scratch", RawType: "tmpfs",
			Reason: model.SkipReasonTmpfs, Detail: "non-persistent (tmpfs lives in RAM)"}},
		ServicesWithoutVolumes: []string{"web"},
		// No replica of the introspection has this id: kept by id alone.
		KilledContainers: []model.KilledContainer{{Container: "c0ffee"}},
	}

	registry := &mockRegistry{}

	pgBackend := &mockBackend{volume: model.Volume{ObjectID: "volume-named-demo_pgdata"}}
	snapPg := pgBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: pgBackend.volume, Type: model.SnapshotBackendCopy, Payload: "pg-payload"}, nil).
		Once().NotBefore(stop)
	cfgBackend := &mockBackend{volume: model.Volume{ObjectID: "volume-service-app-etc-cfg"}}
	snapCfg := cfgBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: cfgBackend.volume, Type: model.SnapshotBackendCopy, Payload: "cfg-payload"}, nil).
		Once().NotBefore(stop)

	// Stop → every capture → the spec → commit → start. Save-last is the commit marker,
	// and NotBefore pins it after the whole fan-out and after the artifact it names.
	repo := &mockSavepointRepo{}
	nameCheck := repo.On("ListSavepoints", mock.Anything).
		Return([]model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "other"}}, nil).Once()
	saveSpec := expectSpecCommit(repo).NotBefore(snapPg, snapCfg)
	save := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil && r.Name == "before-migration" && r.Description == "trying a schema change" &&
			r.Spec != nil && r.Spec.Format == model.SpecFormatComposeYAML &&
			assert.ObjectsAreEqual(wantNotCovered, r.NotCovered)
	})).Return(nil).Once().NotBefore(saveSpec)
	runner.On("Start", mock.Anything, demoSpec, 90*time.Second).Return(nil).Once().NotBefore(save)

	store := newDirStore(t)
	var specs []model.BackendSpec
	captureSpec := func(args mock.Arguments) { specs = append(specs, args.Get(0).(model.BackendSpec)) }
	newPg := registry.On("New", matchSpec(store, "volume-named-demo_pgdata")).
		Run(captureSpec).Return(pgBackend, nil).Once()
	newCfg := registry.On("New", matchSpec(store, "volume-service-app-etc-cfg")).
		Run(captureSpec).Return(cfgBackend, nil).Once()
	// Every backend is built before the stop: one that cannot be built must leave
	// the project running. Reversed, it compiles.
	stop.NotBefore(newPg, newCfg, nameCheck)

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Name:        "before-migration",
		Description: "trying a schema change",
		WaitTimeout: 90 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	require.NoError(t, savepoint.ID.Validate())
	assert.Equal(t, "before-migration", savepoint.Name)
	assert.Equal(t, "trying a schema change", savepoint.Description)
	assert.False(t, savepoint.CreatedAt.IsZero())
	assert.ElementsMatch(t, []model.SnapshotStat{
		{Volume: pgBackend.volume, Type: model.SnapshotBackendCopy, Payload: "pg-payload"},
		{Volume: cfgBackend.volume, Type: model.SnapshotBackendCopy, Payload: "cfg-payload"},
	}, savepoint.Stats)
	assert.Equal(t, wantNotCovered, savepoint.NotCovered)

	// Every volume captured under the one id the run created, and nothing else was
	// asked of the backends — a create captures into the savepoint store only.
	pgBackend.AssertCalled(t, "Snapshot", mock.Anything, model.BackendSnapshotRequest{SnapshotID: savepoint.ID})
	cfgBackend.AssertCalled(t, "Snapshot", mock.Anything, model.BackendSnapshotRequest{SnapshotID: savepoint.ID})

	// The store reaches every backend as the DI root left it — there is one, and a
	// backend bound to anything else would be writing outside the project's tree.
	require.Len(t, specs, 2)
	for _, spec := range specs {
		assert.Same(t, store, spec.Store)
	}

	// The record committed is the one returned.
	repo.AssertCalled(t, "SaveSavepoint", mock.Anything, savepoint)

	assert.Contains(t, logs.String(), "volume will not be backed up")
	assert.Contains(t, logs.String(), "/scratch")
	assert.Contains(t, logs.String(), "tmpfs")
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	pgBackend.AssertExpectations(t)
	cfgBackend.AssertExpectations(t)
}

// Labels reach every backend with the capture request and land in the record.
func TestService_CreateSavepoint_CarriesLabels(t *testing.T) {
	// Arrange
	labels := map[string]string{"team": "payments"}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil).Once()
	repo := &mockSavepointRepo{}
	expectSpecCommit(repo)
	repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.Labels["team"] == "payments"
	})).Return(nil).Once()

	backend := &mockBackend{volume: demoVolume()}
	backend.On("Snapshot", mock.Anything, mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil && req.Labels["team"] == "payments"
	})).Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendCopy}, nil).Once()
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Labels: labels,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, labels, savepoint.Labels)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// Every replica's anonymous volume is a unit of its own, captured by its own backend.
func TestService_CreateSavepoint_AnonymousVolumePerReplica(t *testing.T) {
	// Arrange
	anon := func(replica int) model.ServiceVolume {
		return model.ServiceVolume{Type: model.VolumeTypeAnon, RawType: "volume", Target: "/cache", Replica: replica}
	}
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{{
			Name:    "worker",
			Volumes: []model.ServiceVolume{anon(1), anon(2), anon(3)},
		}},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil).Once()
	repo := &mockSavepointRepo{}
	expectSpecCommit(repo)
	repo.On("SaveSavepoint", mock.Anything, mock.Anything).Return(nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	var backends []*mockBackend
	for replica := 1; replica <= 3; replica++ {
		objectID := fmt.Sprintf("volume-anon-worker-%d-cache", replica)
		backend := &mockBackend{volume: model.Volume{ObjectID: objectID}}
		backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
			Return(model.SnapshotStat{Volume: backend.volume, Type: model.SnapshotBackendCopy}, nil).Once()
		registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
			return spec.Store == store && spec.Volume.ObjectID == objectID &&
				spec.Volume.Anon != nil && spec.Volume.Anon.Replica == replica
		})).Return(backend, nil).Once()
		backends = append(backends, backend)
	}

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Len(t, savepoint.Stats, 3)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	for _, backend := range backends {
		backend.AssertExpectations(t)
	}
}

// The backend a new savepoint captures with is the configured one, not a
// constant: it is stamped on every volume the create resolves.
func TestService_CreateSavepoint_StampsTheConfiguredBackend(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil).Once()
	repo := &mockSavepointRepo{}
	expectSpecCommit(repo)
	repo.On("SaveSavepoint", mock.Anything, mock.Anything).Return(nil).Once()

	cdcVolume := model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendCDC}
	backend := &mockBackend{volume: cdcVolume}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: cdcVolume, Type: model.SnapshotBackendCDC}, nil).Once()
	registry := &mockRegistry{}
	registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Volume.Backend == model.SnapshotBackendCDC
	})).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            newDirStore(t),
		DefaultBackend:   model.SnapshotBackendCDC,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, savepoint.Stats, 1)
	assert.Equal(t, model.SnapshotBackendCDC, savepoint.Stats[0].Volume.Backend)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

func TestService_CreateSavepoint_AnAssemblerFailureLeavesTheComposeProjectRunning(t *testing.T) {
	// Arrange
	assembleErr := errors.New("compose files are gone")
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	assembler := &mockAssembler{}
	assembler.On("Assemble", mock.Anything, demoIntrospection()).Return(nil, assembleErr).Once()
	runner := &mockComposeRunner{}

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    &mockSavepointRepo{},
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  &mockRegistry{},
		Inflight:         &fakeInflight{},
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	// The runner carries no expectations: a stop would fail the run where it happened.
	require.ErrorIs(t, err, assembleErr)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// A backend without a factory fails before the services are stopped.
func TestService_CreateSavepoint_UnknownBackend(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	repo := &mockSavepointRepo{}
	registry := &mockRegistry{}
	registry.On("New", matchSpec(nil, "volume-named-demo_db")).Return(nil, ErrUnknownBackend)

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            nil,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	// The runner and the repository carry no expectations: a stop, a restart or a
	// commit would each fail the run where it happened.
	require.ErrorIs(t, err, ErrUnknownBackend)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A capture that fails commits nothing: the savepoint stays invisible rather
// than listing with half its volumes, and the services still come back up.
func TestService_CreateSavepoint_UncommittedOnFailure(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil).Once()
	repo := &mockSavepointRepo{}

	backend := &mockBackend{volume: model.Volume{ObjectID: "volume-named-demo_db"}}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{}, errors.New("capture boom")).Once()
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capture boom")
	// Nothing was committed: repo carries no Save expectation, so a commit would
	// fail the run where it happened.
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// The bytes a half-finished fan-out did write stay for a later gc: no record names
// them, so nothing reads them, and a failure path never deletes data. Driven through
// the real copy backend over a real store, since a mocked capture writes nothing.
func TestService_CreateSavepoint_RetainsWhatAPartialCaptureWrote(t *testing.T) {
	// Arrange
	// Two units: the named volume captures, the bind mount's own service fails.
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{{
			Name: "app",
			Volumes: []model.ServiceVolume{{
				Type: model.VolumeTypeBind, RawType: "bind", Source: "/host/cfg", Target: "/etc/cfg",
			}},
		}},
		Volumes: []model.NamedVolume{{Key: "db", Name: "demo_db",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}}},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil)

	store := newDirStore(t)
	// The failure waits for the surviving archive: its cancel would abort a put still in flight.
	docker := &fakeVolumeStreamer{
		payload:        "pgdata bytes",
		openErrFor:     "volume-service-app-etc-cfg",
		failAfterClose: make(chan struct{}),
	}
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(docker))
	require.NoError(t, err)
	repo := savepointrepo.New(store)
	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err = svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.Error(t, err)
	names := objectNames(t, store)
	require.Len(t, names, 1, "the archive the surviving volume wrote is kept for a gc")
	assert.True(t, strings.HasSuffix(names[0], "/volume-named-demo_db/data.tar"), "kept = %v", names)
	// And it lists as nothing: no record names the directory, so it is invisible.
	committed, err := repo.ListSavepoints(ctx)
	require.NoError(t, err)
	assert.Empty(t, committed)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// A create that would cover nothing is refused before the services are touched:
// stopping a project to capture zero volumes is pure downtime.
func TestService_CreateSavepoint_NoVolumes(t *testing.T) {
	// Arrange
	empty := model.IntrospectionResult{Project: model.Project{
		Name:     demoProject,
		Services: []model.Service{{Name: demoService}},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(empty, nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    &mockSavepointRepo{},
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(empty),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            nil,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	// Neither the runner nor the registry carries an expectation: a create that
	// covers nothing must not stop the project or open a bracket.
	require.ErrorIs(t, err, errNoVolumesToBackUp)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A compose project taken down has nothing to capture a spec of: the create is refused
// before anything is assembled, even though its volumes still exist.
func TestService_CreateSavepoint_ComposeProjectAbsent(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospection(), nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	assembler := &mockAssembler{}
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}
	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    &mockSavepointRepo{},
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        &mockCodec{},
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.ErrorIs(t, err, errComposeProjectAbsent)
	assert.Contains(t, err.Error(), `compose project "demo"`)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	assembler.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// A name the savepoint could not carry is refused while the project still runs:
// nothing is introspected, stopped or captured.
func TestService_CreateSavepoint_RefusesItsName(t *testing.T) {
	tests := []struct {
		name     string
		reqName  string
		existing []model.Savepoint
		wantErr  error
	}{
		{name: "a slash", reqName: "feat/v1", wantErr: model.ErrInvalidSavepointName},
		{name: "a savepoint id", reqName: "019e843e-1650-7000-8000-000000000000", wantErr: model.ErrInvalidSavepointName},
		{
			name:     "a name already taken",
			reqName:  "v1",
			existing: []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "v1"}},
			wantErr:  model.ErrSavepointNameTaken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			expectLock(locker)
			repo := &mockSavepointRepo{}
			if tt.existing != nil {
				repo.On("ListSavepoints", mock.Anything).Return(tt.existing, nil).Once()
			}
			inspector := &mockInspector{}
			runner := &mockComposeRunner{}
			svc := New(Deps{
				SavepointRepo:    repo,
				ProjectLocker:    locker,
				ComposeInspector: inspector,
				ComposeRunner:    runner,
				Inflight:         &fakeInflight{},
				Store:            newDirStore(t),
			})

			// Act
			_, err := svc.CreateSavepoint(zerolog.Nop().WithContext(context.Background()),
				model.CreateSavepointRequest{Name: tt.reqName})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			locker.AssertExpectations(t)
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			runner.AssertExpectations(t)
		})
	}
}

func TestService_requireUniqueName(t *testing.T) {
	const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
	safety := model.Savepoint{ID: id, Name: "pre-rollback", Labels: safetySnapshotLabels()}

	tests := []struct {
		name     string
		reqName  string
		existing []model.Savepoint
		wantErr  error
	}{
		{name: "no name reads nothing"},
		{name: "a free name", reqName: "v2", existing: []model.Savepoint{{ID: id, Name: "v1"}}},
		{name: "a taken name", reqName: "v1", existing: []model.Savepoint{{ID: id, Name: "v1"}}, wantErr: model.ErrSavepointNameTaken},
		{name: "a safety snapshot's name does not count", reqName: "pre-rollback", existing: []model.Savepoint{safety}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepointRepo{}
			if tt.reqName != "" {
				repo.On("ListSavepoints", mock.Anything).Return(tt.existing, nil).Once()
			}
			svc := New(Deps{SavepointRepo: repo})

			// Act
			err := svc.requireUniqueName(context.Background(), tt.reqName)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			repo.AssertExpectations(t)
		})
	}
}

// An orphan tree a crashed run left under the id stops the capture: it is the
// only copy of whatever that run captured, so it is reported, never deleted.
func TestService_requireEmptyTree(t *testing.T) {
	tests := []struct {
		name    string
		planted []string
		wantErr error
	}{
		{name: "an empty directory passes"},
		{name: "an orphan is refused", planted: []string{"volume-named-gone/data.tar"}, wantErr: errCaptureDirectoryNotEmpty},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
			store := newDirStore(t)
			want := make([]string, 0, len(tt.planted))
			for _, name := range tt.planted {
				key := id.String() + "/" + name
				require.NoError(t, store.PutObject(context.Background(), key, strings.NewReader("stale")))
				want = append(want, key)
			}
			svc := New(Deps{Store: store})

			// Act
			err := svc.requireEmptyTree(zerolog.Nop().WithContext(context.Background()), id)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.ElementsMatch(t, want, objectNames(t, store), "nothing is deleted")
		})
	}
}

// A named volume no service mounts is covered like any other: a backend
// addresses a volume by name, so having no container to read it through is no
// longer a reason to leave it out of the savepoint.
func TestService_CreateSavepoint_CoversAnUnmountedVolume(t *testing.T) {
	// Arrange
	introspection := model.IntrospectionResult{Project: model.Project{
		Name:     "demo",
		Services: demoIntrospection().Project.Services,
		Volumes: []model.NamedVolume{
			{Key: "db", Name: "demo_db", UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}},
			// Mounted by no service — docker created it, so it can hold data.
			{Key: "orphan", Name: "demo_orphan"},
		},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil)
	repo := &mockSavepointRepo{}
	repo.On("SaveSpec", mock.Anything, mock.Anything, demoSpecBytes).Return(nil)
	repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil
	})).Return(nil)

	store := newDirStore(t)
	registry := &mockRegistry{}
	backends := make([]*mockBackend, 0, 2)
	for _, objectID := range []string{"volume-named-demo_db", "volume-named-demo_orphan"} {
		backend := &mockBackend{volume: model.Volume{ObjectID: objectID}}
		backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
			Return(model.SnapshotStat{Volume: backend.volume, Type: model.SnapshotBackendCopy}, nil).Once()
		registry.On("New", matchSpec(store, objectID)).Return(backend, nil).Once()
		backends = append(backends, backend)
	}

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, savepoint.Stats, 2)
	assert.ElementsMatch(t, []string{"volume-named-demo_db", "volume-named-demo_orphan"},
		[]string{savepoint.Stats[0].Volume.ObjectID, savepoint.Stats[1].Volume.ObjectID})
	assert.NotContains(t, logs.String(), "volume will not be backed up",
		"nothing is left out for being unreachable any more")
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	for _, backend := range backends {
		backend.AssertExpectations(t)
	}
}

// The id is the creation second, and the record lands in the directory that id
// names — through the real repository, real store and real copy backend.
func TestService_CreateSavepoint_CreatesTheIDFromTheClock(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil)
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil)

	store := newDirStore(t)
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(&fakeVolumeStreamer{payload: "pgdata bytes"}))
	require.NoError(t, err)
	repo := savepointrepo.New(store)
	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), savepoint.CreatedAt, time.Minute)
	assert.NoError(t, savepoint.ID.Validate(), "the id doubles as a path-safe directory")
	assert.ElementsMatch(t,
		[]string{
			savepoint.ID.String() + "/savepoint.json",
			savepoint.ID.String() + "/project.yaml",
			savepoint.ID.String() + "/volume-named-demo_db/data.tar",
		},
		objectNames(t, store),
		"the metadata and the spec are committed into the same tree as the captures")

	// And it reads back: what the caller got is what the store holds.
	committed, err := repo.ListSavepoints(ctx)
	require.NoError(t, err)
	require.Len(t, committed, 1)
	assert.Equal(t, savepoint.ID, committed[0].ID)
	assert.Len(t, committed[0].Stats, 1)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
}

// A savepoint whose services failed to restart stays committed, but the return
// is the zero record: an error means no value, and the error names the id.
func TestService_CreateSavepoint_CommittedWhenStartFails(t *testing.T) {
	// Arrange
	vol := demoVolume()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()

	backend := &mockBackend{volume: vol}
	snap := backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendCopy, Payload: "pg-payload"}, nil).
		Once()
	repo := &mockSavepointRepo{}
	saveSpec := expectSpecCommit(repo)
	var savedID model.SavepointID
	save := repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil
	})).Run(func(args mock.Arguments) {
		savedID = args.Get(1).(model.Savepoint).ID
	}).Return(nil).Once()
	up := runner.On("Start", mock.Anything, demoSpec, 60*time.Second).
		Return(errors.New("unhealthy")).Once()
	mock.InOrder(stop, snap, saveSpec, save, up)

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		WaitTimeout: 60 * time.Second,
	})

	// Assert: the record is committed, so neither Delete nor Remove is expected above.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committed; services failed to start")
	assert.Contains(t, err.Error(), savedID.String())
	assert.Zero(t, savepoint)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// A safety snapshot is an ordinary record in the one store, so what keeps it out
// of a listing is the flag and nothing structural.
func TestService_ListSavepoints(t *testing.T) {
	tests := []struct {
		name string
		req  model.ListRequest
		want []model.Savepoint
	}{
		{name: "hidden by default", req: model.ListRequest{}, want: []model.Savepoint{demoRecord()}},
		{
			name: "shown with the flag",
			req:  model.ListRequest{IncludeSafetySnapshots: true},
			want: []model.Savepoint{demoRecord(), demoSafetySnapshot()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepointRepo{}
			repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{demoRecord(), demoSafetySnapshot()}, nil).Once()
			svc := New(Deps{
				VolumeRemover: &mockVolumeRemover{},
				SavepointRepo: repo, ProjectLocker: nil, ComposeInspector: nil,
				ComposeRunner: nil, BackendRegistry: nil, Inflight: &fakeInflight{},
				Store: nil, DefaultBackend: model.SnapshotBackendCopy,
			})

			// Act
			got, err := svc.ListSavepoints(zerolog.Nop().WithContext(context.Background()), tt.req)

			// Assert
			// No lock is taken and no backend built: neither collaborator is even
			// supplied, so reaching for one would panic.
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			repo.AssertExpectations(t)
		})
	}
}

// A scanning selector never sees a safety snapshot, so retention neither counts one
// among the newest it keeps nor deletes one it never meant. An explicit id does
// see it, and that is how the shard of a killed rollback is cleared.
func TestService_RemoveSavepoints_SafetySnapshotVisibility(t *testing.T) {
	record, backup := demoRecord(), demoSafetySnapshot()

	tests := []struct {
		name string
		req  model.RemoveRequest
		want []model.Savepoint
	}{
		{
			// The backup is the newest of the two, so counting it would retain it and
			// delete the savepoint instead — exactly backwards.
			name: "retain-recent 1 keeps the savepoint and leaves the backup alone",
			req:  model.RemoveRequest{RetainRecent: 1, DryRun: true},
			want: nil,
		},
		{
			name: "an age rule sweeps neither",
			req:  model.RemoveRequest{BeforeDate: "2026-06-12T11:30:00Z", DryRun: true},
			want: []model.Savepoint{record},
		},
		{
			name: "an explicit id reaches the backup",
			req:  model.RemoveRequest{IDs: []model.SavepointID{backup.ID}, DryRun: true},
			want: []model.Savepoint{backup},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepointRepo{}
			repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record, backup}, nil).Once()
			svc := New(Deps{
				VolumeRemover: &mockVolumeRemover{},
				SavepointRepo: repo, ProjectLocker: nil, ComposeInspector: nil,
				ComposeRunner: nil, BackendRegistry: nil, Inflight: &fakeInflight{},
				Store: nil, DefaultBackend: model.SnapshotBackendCopy,
			})

			// Act
			res, err := svc.RemoveSavepoints(zerolog.Nop().WithContext(context.Background()), tt.req)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.Removed)
			repo.AssertExpectations(t)
		})
	}
}

// Removal is uncommit-first and sweeps afterwards. Both orders compile when
// reversed, so they are pinned here.
func TestService_RemoveSavepoints(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	uncommit := repo.On("DeleteSavepoint", mock.Anything, record.ID).Return(nil).Once()

	// A remove carries the payload the write reported: only its own backend
	// reads one. NotBefore pins uncommit-first: data-first would leave a
	// savepoint that still lists but is half gone.
	backend := &mockBackend{volume: vol}
	backend.On("Remove", mock.Anything, model.BackendRemoveRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(nil).Once().NotBefore(uncommit)
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	// An orphan of a crashed create: no stat names it, only the sweep reclaims it.
	require.NoError(t, store.PutObject(context.Background(),
		record.ID.String()+"/volume-named-orphan/data.tar", strings.NewReader("orphan")))

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: nil,
		ComposeRunner: nil, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []model.SavepointID{record.ID}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	assert.Empty(t, objectNames(t, store), "the sweep reclaims what no stat named")
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// A savepoint whose data cannot be dropped is still uncommitted; the orphan is
// named in the log.
func TestService_RemoveSavepoints_DataFailureLeavesAnOrphan(t *testing.T) {
	// Arrange
	vol := demoVolume()
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("DeleteSavepoint", mock.Anything, record.ID).Return(nil).Once()

	backend := &mockBackend{volume: vol}
	backend.On("Remove", mock.Anything, model.BackendRemoveRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("remove boom")).Once()
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: nil,
		ComposeRunner: nil, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: store, DefaultBackend: model.SnapshotBackendCopy,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []model.SavepointID{record.ID}})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove boom")
	assert.Contains(t, logs.String(), "the directory is orphaned")
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// A dry run takes no lock, deletes no record and reaches no backend.
func TestService_RemoveSavepoints_DryRun(t *testing.T) {
	// Arrange
	record := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "sp", CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC)}
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	locker := &mockLocker{}
	registry := &mockRegistry{}

	svc := New(Deps{
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: nil,
		ComposeRunner: nil, BackendRegistry: registry, Inflight: &fakeInflight{},
		Store: nil, DefaultBackend: model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []model.SavepointID{record.ID}, DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	// Only List is expected: a Delete, an Acquire or a backend build would each
	// fail the run where it happened.
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// TestValidateRemoveRequest covers the selector rules. A negative --retain-recent
// must be reported as such, not as a missing selector.
func TestValidateRemoveRequest(t *testing.T) {
	tests := []struct {
		name string
		req  model.RemoveRequest
		want error
	}{
		{name: "explicit ids", req: model.RemoveRequest{IDs: []model.SavepointID{"1"}}},
		{name: "retain recent", req: model.RemoveRequest{RetainRecent: 2}},
		{name: "retain for", req: model.RemoveRequest{RetainFor: "1d"}},
		{
			name: "ids and retention conflict",
			req:  model.RemoveRequest{IDs: []model.SavepointID{"1"}, RetainRecent: 2},
			want: model.ErrRemoveSelectorConflict,
		},
		{name: "no selector at all", req: model.RemoveRequest{}, want: model.ErrRemoveNoSelector},
		{
			name: "negative retain recent",
			req:  model.RemoveRequest{RetainRecent: -1},
			want: model.ErrRetainRecentNegative,
		},
		{
			name: "negative retain recent beats a conflicting id list",
			req:  model.RemoveRequest{IDs: []model.SavepointID{"1"}, RetainRecent: -1},
			want: model.ErrRetainRecentNegative,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := validateRemoveRequest(tt.req)

			// Assert
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

// TestService_retentionCutoffs pins the two rules against each other: --retain-for
// counts back from the wall clock, --before-date is taken verbatim, and both
// together keep that order. A relative cutoff is checked against the clock the
// call itself read, so the tolerance is the test's own runtime.
func TestService_retentionCutoffs(t *testing.T) {
	beforeDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		req     model.RemoveRequest
		want    []time.Duration // a cutoff relative to now; zero means "read it from wantAbsolute"
		wantAbs map[int]time.Time
		wantErr bool
	}{
		{name: "no rules yields no cutoffs", req: model.RemoveRequest{}},
		{
			name: "retain-for subtracts the duration from now",
			req:  model.RemoveRequest{RetainFor: "2d"},
			want: []time.Duration{-48 * time.Hour},
		},
		{
			name:    "before-date is used verbatim",
			req:     model.RemoveRequest{BeforeDate: "2026-06-01T00:00:00Z"},
			want:    []time.Duration{0},
			wantAbs: map[int]time.Time{0: beforeDate},
		},
		{
			name:    "both rules yield both cutoffs, retain-for first",
			req:     model.RemoveRequest{RetainFor: "1h", BeforeDate: "2026-06-01T00:00:00Z"},
			want:    []time.Duration{-time.Hour, 0},
			wantAbs: map[int]time.Time{1: beforeDate},
		},
		{name: "unparsable retain-for errors", req: model.RemoveRequest{RetainFor: "yesterday"}, wantErr: true},
		{name: "unparsable before-date errors", req: model.RemoveRequest{BeforeDate: "june"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := New(Deps{
				VolumeRemover: &mockVolumeRemover{},
				SavepointRepo: nil, ProjectLocker: nil, ComposeInspector: nil,
				ComposeRunner: nil, BackendRegistry: nil, Inflight: &fakeInflight{},
				Store: nil, DefaultBackend: model.SnapshotBackendCopy,
			})
			now := time.Now()

			// Act
			got, err := svc.retentionCutoffs(tt.req)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, len(tt.want))
			for i, offset := range tt.want {
				if want, ok := tt.wantAbs[i]; ok {
					assert.True(t, got[i].Equal(want), "cutoff[%d] = %v, want %v", i, got[i], want)
					continue
				}
				assert.WithinDuration(t, now.Add(offset), got[i], time.Minute, "cutoff[%d] = %v", i, got[i])
			}
		})
	}
}

func TestPartitionSavepoints(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	mid := now.Add(-7 * 24 * time.Hour)
	fresh := now.Add(-time.Hour)
	records := recordsAt(old, mid, fresh)

	tests := []struct {
		name         string
		retainRecent int
		cutoffs      []time.Time
		wantRemoved  []string
	}{
		{
			name:         "retain recent keeps newest N",
			retainRecent: 2,
			wantRemoved:  []string{records[0].ID.String()},
		},
		{
			name:         "retain recent zero removes all",
			retainRecent: 0,
			wantRemoved:  ids(records),
		},
		{
			name:        "cutoff removes older only",
			cutoffs:     []time.Time{now.Add(-14 * 24 * time.Hour)},
			wantRemoved: []string{records[0].ID.String()},
		},
		{
			name:         "any rule retaining wins",
			retainRecent: 3,
			cutoffs:      []time.Time{now}, // cutoff alone would remove everything
			wantRemoved:  nil,
		},
		{
			name:         "removed only when all rules agree",
			retainRecent: 1,
			cutoffs:      []time.Time{now.Add(-14 * 24 * time.Hour)},
			wantRemoved:  []string{records[0].ID.String()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			kept, removed := partitionSavepoints(records, tt.retainRecent, tt.cutoffs)

			// Assert
			assert.Equal(t, tt.wantRemoved, ids(removed))
			assert.Len(t, kept, len(records)-len(removed), "kept + removed must cover every record")
		})
	}
}

func TestFindSavepoint(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	// The safety snapshot is the newest record and shares a name with two others, so
	// it is in reach of every scanning selector — and must be picked by none.
	safetySnapshot := model.Savepoint{
		ID: "400", Name: "dup", Labels: safetySnapshotLabels(), CreatedAt: now.Add(3 * time.Hour),
	}
	records := []model.Savepoint{
		{ID: "100", Name: "before-migration", CreatedAt: now},
		{ID: "300", Name: "dup", CreatedAt: now.Add(2 * time.Hour)}, // newest visible
		{ID: "200", Name: "dup", CreatedAt: now.Add(time.Hour)},
		safetySnapshot,
	}

	latest := model.RollbackRequest{Latest: ptrBool(true)}
	byName := func(name string) model.RollbackRequest { return model.RollbackRequest{Name: &name} }

	tests := []struct {
		name    string
		records []model.Savepoint
		req     model.RollbackRequest
		wantID  model.SavepointID
		wantErr bool
	}{
		{name: "latest picks newest by created_at", records: records, req: latest, wantID: "300"},
		{name: "explicit id hit", records: records, req: model.RollbackRequest{ID: ptrID("200")}, wantID: "200"},
		{name: "unique name hit", records: records, req: byName("before-migration"), wantID: "100"},
		{name: "ambiguous name errors", records: records, req: byName("dup"), wantErr: true},
		{name: "unknown id errors", records: records, req: model.RollbackRequest{ID: ptrID("999")}, wantErr: true},
		{name: "unknown name errors", records: records, req: byName("nope"), wantErr: true},
		// An id is exact and never falls back to a name: a savepoint *named* "300"
		// is not what `rollback 300` asks for.
		{
			name:    "an id never resolves by name",
			records: []model.Savepoint{{ID: "100", Name: "300", CreatedAt: now}},
			req:     model.RollbackRequest{ID: ptrID("300")},
			wantErr: true,
		},
		// The manual recovery path after a killed rollback: the id was read off the
		// log or off `savepoint list --include-safety-snapshots`, and naming it is the
		// caller's statement that they know what they are asking for.
		{name: "an explicit id reaches a safety snapshot", records: records, req: model.RollbackRequest{ID: ptrID("400")}, wantID: "400"},
		{
			name:    "latest never lands on a safety snapshot",
			records: []model.Savepoint{{ID: "100", CreatedAt: now}, safetySnapshot},
			req:     latest,
			wantID:  "100",
		},
		{
			name:    "nor does a name that only it carries",
			records: []model.Savepoint{{ID: "100", Name: "sp", CreatedAt: now}, safetySnapshot},
			req:     byName("dup"),
			wantErr: true,
		},
		{
			name:    "a store holding only safety snapshots reads as empty",
			records: []model.Savepoint{safetySnapshot},
			req:     latest,
			wantErr: true,
		},
		{name: "no savepoints at all errors", req: latest, wantErr: true},
		// The backstop for a request that skipped Validate: no selector resolves to
		// no savepoint, not to an arbitrary one.
		{name: "no selector errors", records: records, req: model.RollbackRequest{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := findSavepoint(tt.records, tt.req)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}
}

func TestFindSavepoint_AmbiguousName(t *testing.T) {
	// Arrange: two visible "dup"s out of id order, plus a safety snapshot of the same name.
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	name := "dup"
	records := []model.Savepoint{
		{ID: "300", Name: name, CreatedAt: now},
		{ID: "200", Name: name, CreatedAt: now},
		{ID: "400", Name: name, Labels: safetySnapshotLabels(), CreatedAt: now},
	}

	// Act
	_, err := findSavepoint(records, model.RollbackRequest{Name: &name})

	// Assert
	var ambiguous *model.AmbiguousSavepointNameError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, name, ambiguous.Name)
	assert.Equal(t, []model.SavepointID{"200", "300"}, ambiguous.IDs, "sorted, and never the safety snapshot")
	assert.EqualError(t, err, `2 savepoints are named "dup"; roll back by id instead: 200, 300`)
}

// recordsAt plants one record per instant, each id a UUIDv7 of its creation time.
func recordsAt(times ...time.Time) []model.Savepoint {
	records := make([]model.Savepoint, len(times))
	for i, t := range times {
		records[i] = model.Savepoint{ID: savepointIDAt(t), CreatedAt: t}
	}
	return records
}

// savepointIDAt is the UUIDv7 a capture started at t would sort as.
func savepointIDAt(t time.Time) model.SavepointID {
	h := fmt.Sprintf("%012x", t.UnixMilli())
	return model.SavepointID(h[:8] + "-" + h[8:] + "-7000-8000-000000000000")
}

func ids(records []model.Savepoint) []string {
	if len(records) == 0 {
		return nil
	}
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID.String()
	}
	return out
}

// newDirStore is a real directory Storager on a tempdir: the service addresses
// its stores directly, so a nil one would panic.
func newDirStore(t *testing.T) storages.Storager {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

// objectNames lists a store's whole tree.
func objectNames(t *testing.T, store storages.Storager) []string {
	t.Helper()

	written, err := store.List(context.Background(), "")
	require.NoError(t, err)
	names := make([]string, 0, len(written))
	for _, obj := range written {
		names = append(names, obj.Name)
	}
	return names
}

// fakeVolumeStreamer serves a fixed payload from OpenVolume and collects
// WriteVolume's stream; hand-rolled because a mock cannot stream. Guarded, since
// the fan-out is concurrent.
//
// It addresses every volume by its own ObjectID, which no real engine does — it
// only has to be unique per volume, so a case can name the one that fails.
type fakeVolumeStreamer struct {
	mu      sync.Mutex
	payload string
	written strings.Builder
	// openErrFor is the ObjectID of the volume that will not open, for a test
	// that needs one volume of several to fail.
	openErrFor string
	// failAfterClose, when set, holds the failing open until a served stream is
	// closed — after its archive is in place — so the failure cancels no write.
	failAfterClose chan struct{}
	closeOnce      sync.Once
}

func (f *fakeVolumeStreamer) ResolveMountSource(
	_ context.Context, v model.Volume,
) (model.MountSource, error) {
	return model.MountSource{Type: model.MountSourceVolume, Source: v.ObjectID}, nil
}

func (f *fakeVolumeStreamer) OpenVolume(
	_ context.Context, src model.MountSource,
) (io.ReadCloser, error) {
	if src.Source != "" && src.Source == f.openErrFor {
		if f.failAfterClose != nil {
			<-f.failAfterClose
		}
		return nil, errors.New("volume boom")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &fakeVolumeStream{Reader: strings.NewReader(f.payload), streamer: f}, nil
}

// fakeVolumeStream releases a failing open waiting on failAfterClose.
type fakeVolumeStream struct {
	io.Reader
	streamer *fakeVolumeStreamer
}

func (s *fakeVolumeStream) Close() error {
	if s.streamer.failAfterClose != nil {
		s.streamer.closeOnce.Do(func() { close(s.streamer.failAfterClose) })
	}
	return nil
}

func (f *fakeVolumeStreamer) WriteVolume(_ context.Context, _ model.MountSource, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := io.Copy(&f.written, r)
	return err
}

// A stop or a capture that fails — an interrupt included — brings the services back
// on a live context: the Ctrl-C that aborted the capture must not leave them stopped.
func TestService_CreateSavepoint_RestartsAfterAnInterrupt(t *testing.T) {
	tests := []struct {
		name    string
		stopErr error
		wantErr string
	}{
		{
			name:    "stop fails partway",
			stopErr: context.Canceled,
			wantErr: "interrupted: context canceled",
		},
		{
			name:    "capture interrupted",
			wantErr: "capturing volumes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
			defer cancel()

			inspector := &mockInspector{}
			inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
			locker := &mockLocker{}
			expectLock(locker)

			var startErr error
			runner := &mockComposeRunner{}
			runner.On("Stop", mock.Anything, demoSpec).Run(func(mock.Arguments) { cancel() }).
				Return([]string(nil), tt.stopErr).Once()
			runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).
				Run(func(args mock.Arguments) { startErr = args.Get(0).(context.Context).Err() }).
				Return(nil).Once()

			store := newDirStore(t)
			backend := &mockBackend{volume: model.Volume{ObjectID: "volume-named-demo_db"}}
			registry := &mockRegistry{}
			registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(backend, nil).Once()
			if tt.stopErr == nil {
				backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
					Return(model.SnapshotStat{}, context.Canceled).Once()
			}

			svc := New(Deps{
				VolumeRemover:    &mockVolumeRemover{},
				SavepointRepo:    &mockSavepointRepo{},
				ProjectLocker:    locker,
				ComposeInspector: inspector,
				ProjectAssembler: demoAssembler(),
				SpecCodec:        demoCodec(),
				ComposeRunner:    runner,
				BackendRegistry:  registry,
				Inflight:         &fakeInflight{},
				Store:            store,
				DefaultBackend:   model.SnapshotBackendCopy,
			})

			// Act
			_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

			// Assert
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NoError(t, startErr, "the restart must run on a live context")
			inspector.AssertExpectations(t)
			locker.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
			backend.AssertExpectations(t)
		})
	}
}
