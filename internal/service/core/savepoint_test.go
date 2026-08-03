package core

import (
	"context"
	"errors"
	"io"
	"strconv"
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
	// One named volume and one bind mount are units; the tmpfs mount is a skip.
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
		},
		Volumes: []model.NamedVolume{{
			Key: "pgdata", Name: "demo_pgdata",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/var/lib/postgresql/data"}},
		}},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, []string{"docker-compose.yml"}).Return(introspection, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	stop := runner.On("Stop", mock.Anything, []string{"docker-compose.yml"}).Return(nil).Once()

	registry := &mockRegistry{}

	pgBackend := &mockBackend{volume: model.Volume{ObjectID: "volume/demo_pgdata"}}
	snapPg := pgBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: pgBackend.volume, Type: model.SnapshotBackendCopy, Payload: "pg-payload"}, nil).
		Once().NotBefore(stop)
	cfgBackend := &mockBackend{volume: model.Volume{ObjectID: "service/app/etc/cfg"}}
	snapCfg := cfgBackend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: cfgBackend.volume, Type: model.SnapshotBackendCopy, Payload: "cfg-payload"}, nil).
		Once().NotBefore(stop)

	// Stop → every capture → commit → start. Save-last is the commit marker, and
	// NotBefore(both captures) pins that it lands only after the whole fan-out;
	// every neighbouring pair compiles when swapped.
	repo := &mockSavepointRepo{}
	save := repo.On("Save", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil && r.Name == "before-migration" && r.Description == "trying a schema change"
	})).Return(nil).Once().NotBefore(snapPg, snapCfg)
	runner.On("Up", mock.Anything, 90*time.Second, []string{"docker-compose.yml"}).Return(nil).Once().NotBefore(save)

	session, safety := newDirStore(t), newDirStore(t)
	var specs []model.BackendSpec
	captureSpec := func(args mock.Arguments) { specs = append(specs, args.Get(0).(model.BackendSpec)) }
	newPg := registry.On("New", matchSpec(model.BackendTierSavepoint, "volume/demo_pgdata")).
		Run(captureSpec).Return(pgBackend, nil).Once()
	newCfg := registry.On("New", matchSpec(model.BackendTierSavepoint, "service/app/etc/cfg")).
		Run(captureSpec).Return(cfgBackend, nil).Once()
	// Every backend is built before the stop: one that cannot be built must leave
	// the project running. Reversed, it compiles.
	stop.NotBefore(newPg, newCfg)

	svc := New(repo, locker, inspector, runner, registry, session, safety, model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		ComposeFiles: []string{"docker-compose.yml"},
		Name:         "before-migration",
		Description:  "trying a schema change",
		WaitTimeout:  90 * time.Second,
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

	// Every volume captured under the one id the run created, and nothing else was
	// asked of the backends — a savepoint is a savepoint-tier capture only.
	pgBackend.AssertCalled(t, "Snapshot", mock.Anything, model.BackendSnapshotRequest{SnapshotID: savepoint.ID})
	cfgBackend.AssertCalled(t, "Snapshot", mock.Anything, model.BackendSnapshotRequest{SnapshotID: savepoint.ID})

	// Both roots reach every backend as the DI root left them.
	require.Len(t, specs, 2)
	for _, spec := range specs {
		assert.Same(t, session, spec.SessionStore)
		assert.Same(t, safety, spec.SafetyStore)
	}

	// The record committed is the one returned.
	repo.AssertCalled(t, "Save", mock.Anything, savepoint)

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
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil).Once()
	repo := &mockSavepointRepo{}
	repo.On("Save", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.Labels["team"] == "payments"
	})).Return(nil).Once()

	backend := &mockBackend{volume: demoVolume()}
	backend.On("Snapshot", mock.Anything, mock.MatchedBy(func(req model.BackendSnapshotRequest) bool {
		return req.SnapshotID.Validate() == nil && req.Labels["team"] == "payments"
	})).Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendCopy}, nil).Once()
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, "volume/demo_db")).Return(backend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t), model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		ComposeFiles: demoComposeFiles(),
		Labels:       labels,
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

// The backend a new savepoint captures with is the configured one, not a
// constant: it is stamped on every volume the create resolves.
func TestService_CreateSavepoint_StampsTheConfiguredBackend(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil).Once()
	repo := &mockSavepointRepo{}
	repo.On("Save", mock.Anything, mock.Anything).Return(nil).Once()

	cdcVolume := model.Volume{ObjectID: "volume/demo_db", Backend: model.SnapshotBackendCDC}
	backend := &mockBackend{volume: cdcVolume}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{Volume: cdcVolume, Type: model.SnapshotBackendCDC}, nil).Once()
	registry := &mockRegistry{}
	registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Volume.Backend == model.SnapshotBackendCDC
	})).Return(backend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t), model.SnapshotBackendCDC)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

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

// A backend without a factory fails before the services are stopped.
func TestService_CreateSavepoint_UnknownBackend(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	repo := &mockSavepointRepo{}
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, "volume/demo_db")).Return(nil, ErrUnknownBackend)

	svc := New(repo, locker, inspector, runner, registry, nil, nil, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

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
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil).Once()
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil).Once()
	repo := &mockSavepointRepo{}

	backend := &mockBackend{volume: model.Volume{ObjectID: "volume/demo_db"}}
	backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
		Return(model.SnapshotStat{}, errors.New("capture boom")).Once()
	// The drop carries the nil payload the failed capture reported.
	backend.On("Remove", mock.Anything, mock.MatchedBy(func(req model.BackendRemoveRequest) bool {
		return req.SnapshotID.Validate() == nil && req.Payload == nil
	})).Return(nil).Once()
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, "volume/demo_db")).Return(backend, nil).Once()

	svc := New(repo, locker, inspector, runner, registry, newDirStore(t), nil, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capture boom")
	// No record was written, so there is nothing to uncommit — only data to drop.
	// repo carries no expectation at all, so a Save or a Delete would fail here.
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// The bytes a half-finished fan-out did write go with it: no record will ever
// name them. Driven through the real copy backend over a real store, since a
// mocked capture writes nothing to assert on.
func TestService_CreateSavepoint_DropsWhatAPartialCaptureWrote(t *testing.T) {
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
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(introspection, nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil)

	session := newDirStore(t)
	docker := &fakeVolumeStreamer{payload: "pgdata bytes", openErrFor: "service/app/etc/cfg"}
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(docker))
	require.NoError(t, err)
	repo := savepointrepo.New(session)
	svc := New(repo, locker, inspector, runner, registry, session, newDirStore(t), model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err = svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

	// Assert
	require.Error(t, err)
	assert.Empty(t, objectNames(t, session),
		"the archive the surviving volume wrote is dropped and its directory swept")
	committed, err := repo.List(ctx)
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
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).
		Return(model.IntrospectionResult{Project: model.Project{Name: demoProject}}, nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	registry := &mockRegistry{}

	svc := New(&mockSavepointRepo{}, locker, inspector, runner, registry, nil, nil, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

	// Assert
	// Neither the runner nor the registry carries an expectation: a create that
	// covers nothing must not stop the project or open a bracket.
	require.ErrorIs(t, err, errNoVolumesToBackUp)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

// An orphan tree a crashed create left under the same id stops the create: it
// is the only copy of whatever that run captured, so it is reported, never
// deleted.
func TestService_CreateSavepoint_RefusesANonEmptyDirectory(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil)
	repo := &mockSavepointRepo{}

	// The backend carries no expectations: a capture dispatched over a refused
	// directory fails the test loudly.
	backend := &mockBackend{volume: model.Volume{ObjectID: "volume/demo_db"}}
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, "volume/demo_db")).Return(backend, nil).Once()

	// What the run that died left behind, planted under every id this create can
	// produce, since it takes its id from the clock.
	session := newDirStore(t)
	orphans := make([]string, 0, 2)
	for _, id := range candidateIDs() {
		key := id.String() + "/volume/gone/data.tar"
		require.NoError(t, session.PutObject(context.Background(), key, strings.NewReader("stale")))
		orphans = append(orphans, key)
	}

	svc := New(repo, locker, inspector, runner, registry, session, newDirStore(t), model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

	// Assert
	require.ErrorIs(t, err, errCaptureDirectoryNotEmpty)
	assert.ElementsMatch(t, orphans, objectNames(t, session),
		"the orphan is left untouched: nothing captured, nothing deleted")
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// A named volume no service mounts is covered like any other: a backend
// addresses a volume by name, so having no container to read it through is no
// longer a reason to leave it out of the savepoint.
func TestService_CreateSavepoint_CoversAnUnmountedVolume(t *testing.T) {
	// Arrange
	introspection := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Volumes: []model.NamedVolume{
			{Key: "db", Name: "demo_db", UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}},
			// Mounted by no service — docker created it, so it can hold data.
			{Key: "orphan", Name: "demo_orphan"},
		},
	}}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(introspection, nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil)
	repo := &mockSavepointRepo{}
	repo.On("Save", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
		return r.ID.Validate() == nil
	})).Return(nil)

	registry := &mockRegistry{}
	backends := make([]*mockBackend, 0, 2)
	for _, objectID := range []string{"volume/demo_db", "volume/demo_orphan"} {
		backend := &mockBackend{volume: model.Volume{ObjectID: objectID}}
		backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
			Return(model.SnapshotStat{Volume: backend.volume, Type: model.SnapshotBackendCopy}, nil).Once()
		registry.On("New", matchSpec(model.BackendTierSavepoint, objectID)).Return(backend, nil).Once()
		backends = append(backends, backend)
	}

	svc := New(repo, locker, inspector, runner, registry, newDirStore(t), newDirStore(t), model.SnapshotBackendCopy)
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

	// Assert
	require.NoError(t, err)
	require.Len(t, savepoint.Stats, 2)
	assert.ElementsMatch(t, []string{"volume/demo_db", "volume/demo_orphan"},
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
	inspector.On("Introspect", mock.Anything, demoComposeFiles()).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoComposeFiles()).Return(nil)
	runner.On("Up", mock.Anything, time.Duration(0), demoComposeFiles()).Return(nil)

	session, safety := newDirStore(t), newDirStore(t)
	registry, err := NewSnapshotBackendRegistry(fullcopy.NewFactory(&fakeVolumeStreamer{payload: "pgdata bytes"}))
	require.NoError(t, err)
	repo := savepointrepo.New(session)
	svc := New(repo, locker, inspector, runner, registry, session, safety, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{ComposeFiles: demoComposeFiles()})

	// Assert
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), savepoint.CreatedAt, time.Minute)
	assert.Equal(t, strconv.FormatInt(savepoint.CreatedAt.Unix(), 10), savepoint.ID.String(),
		"the id is the creation second, so it doubles as a path-safe directory")
	assert.ElementsMatch(t,
		[]string{savepoint.ID.String() + "/savepoint.json", savepoint.ID.String() + "/volume/demo_db/data.tar"},
		objectNames(t, session),
		"the metadata is committed into the same tree as the captures")
	assert.Empty(t, objectNames(t, safety), "a savepoint never touches the safety tier")

	// And it reads back: what the caller got is what the store holds.
	committed, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, committed, 1)
	assert.Equal(t, savepoint.ID, committed[0].ID)
	assert.Len(t, committed[0].Stats, 1)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
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
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	uncommit := repo.On("Delete", mock.Anything, record.ID).Return(nil).Once()

	// A remove carries the payload the write reported: only its own backend
	// reads one. NotBefore pins uncommit-first: data-first would leave a
	// savepoint that still lists but is half gone.
	backend := &mockBackend{volume: vol}
	backend.On("Remove", mock.Anything, model.BackendRemoveRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(nil).Once().NotBefore(uncommit)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(backend, nil).Once()

	// An orphan of a crashed create: no stat names it, only the sweep reclaims it.
	session := newDirStore(t)
	require.NoError(t, session.PutObject(context.Background(),
		record.ID.String()+"/volume/orphan/data.tar", strings.NewReader("orphan")))

	svc := New(repo, locker, nil, nil, registry, session, nil, model.SnapshotBackendCopy)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []model.SavepointID{record.ID}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	assert.Empty(t, objectNames(t, session), "the sweep reclaims what no stat named")
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
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("Delete", mock.Anything, record.ID).Return(nil).Once()

	backend := &mockBackend{volume: vol}
	backend.On("Remove", mock.Anything, model.BackendRemoveRequest{SnapshotID: record.ID, Payload: "sp-payload"}).
		Return(errors.New("remove boom")).Once()
	registry := &mockRegistry{}
	registry.On("New", matchSpec(model.BackendTierSavepoint, vol.ObjectID)).Return(backend, nil).Once()

	svc := New(repo, locker, nil, nil, registry, newDirStore(t), nil, model.SnapshotBackendCopy)
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
	record := model.Savepoint{ID: "1780335122", Name: "sp", CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC)}
	repo := &mockSavepointRepo{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	locker := &mockLocker{}
	registry := &mockRegistry{}

	svc := New(repo, locker, nil, nil, registry, nil, nil, model.SnapshotBackendCopy)
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
			svc := New(nil, nil, nil, nil, nil, nil, nil, model.SnapshotBackendCopy)
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
	records := []model.Savepoint{
		{ID: "100", Name: "before-migration", CreatedAt: now},
		{ID: "300", Name: "dup", CreatedAt: now.Add(2 * time.Hour)}, // newest
		{ID: "200", Name: "dup", CreatedAt: now.Add(time.Hour)},
	}

	tests := []struct {
		name    string
		ref     string
		wantID  model.SavepointID
		wantErr bool
	}{
		{name: "latest picks newest by created_at", ref: "latest", wantID: "300"},
		{name: "explicit id hit", ref: "200", wantID: "200"},
		{name: "unique name hit", ref: "before-migration", wantID: "100"},
		{name: "ambiguous name errors", ref: "dup", wantErr: true},
		{name: "unknown ref errors", ref: "999", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := findSavepoint(records, tt.ref)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}

	t.Run("no savepoints at all errors", func(t *testing.T) {
		// Act
		_, err := findSavepoint(nil, "latest")

		// Assert
		require.Error(t, err)
	})
}

// recordsAt plants one record per instant, each id naming its own creation time.
func recordsAt(times ...time.Time) []model.Savepoint {
	records := make([]model.Savepoint, len(times))
	for i, t := range times {
		records[i] = model.Savepoint{ID: model.SavepointID(strconv.FormatInt(t.Unix(), 10)), CreatedAt: t}
	}
	return records
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

// candidateIDs is every id a call entered now can create: this second and the
// next, since the clock may tick between here and the call.
func candidateIDs() []model.SavepointID {
	now := time.Now().Unix()
	return []model.SavepointID{
		model.SavepointID(strconv.FormatInt(now, 10)),
		model.SavepointID(strconv.FormatInt(now+1, 10)),
	}
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
}

func (f *fakeVolumeStreamer) ResolveVolumeAddress(
	_ context.Context, v model.Volume,
) (model.VolumeAddress, error) {
	return model.VolumeAddress{Kind: model.VolumeAddressVolume, Source: v.ObjectID}, nil
}

func (f *fakeVolumeStreamer) OpenVolume(
	_ context.Context, addr model.VolumeAddress,
) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if addr.Source != "" && addr.Source == f.openErrFor {
		return nil, errors.New("volume boom")
	}
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

func (f *fakeVolumeStreamer) WriteVolume(_ context.Context, _ model.VolumeAddress, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := io.Copy(&f.written, r)
	return err
}
