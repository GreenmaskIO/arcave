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
	"slices"
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
	"github.com/greenmaskio/arcave/internal/service/capture"
	"github.com/greenmaskio/arcave/internal/utils"
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
	wantNotCovered := model.NotCovered{
		Mounts: []model.SkippedVolume{{Service: "app", Target: "/scratch", RawType: "tmpfs",
			Reason: model.SkipReasonTmpfs, Detail: "non-persistent (tmpfs lives in RAM)"}},
		ServicesWithoutVolumes: []string{"web"},
	}

	store := newDirStore(t)
	pgBackend := &mockBackend{volume: model.Volume{ObjectID: "volume-named-demo_pgdata"}}
	cfgBackend := &mockBackend{volume: model.Volume{ObjectID: "volume-service-app-etc-cfg"}}
	var specs []model.BackendSpec
	captureSpec := func(args mock.Arguments) { specs = append(specs, args.Get(0).(model.BackendSpec)) }
	registry := &mockRegistry{}
	newPg := registry.On("New", matchSpec(store, "volume-named-demo_pgdata")).
		Run(captureSpec).Return(pgBackend, nil).Once()
	newCfg := registry.On("New", matchSpec(store, "volume-service-app-etc-cfg")).
		Run(captureSpec).Return(cfgBackend, nil).Once()
	repo := &mockSavepointRepo{}
	nameCheck := repo.On("ListSavepoints", mock.Anything).
		Return([]model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "other"}}, nil).Once()

	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000", Name: "before-migration"}
	capturer := &mockCapture{}
	plan := mock.MatchedBy(func(p capture.Plan) bool {
		return p.Project == demoSpec && string(p.Spec) == string(demoSpecBytes) &&
			p.WaitTimeout == 90*time.Second &&
			sameBackends(p.Volumes, pgBackend, cfgBackend) &&
			assert.ObjectsAreEqual(wantNotCovered, p.NotCovered)
	})
	// Every backend is built, and the name checked, before the capture: one that cannot be
	// built must leave the project running. Reversed, it compiles.
	hold, _, _ := expectCapture(capturer, plan, capture.Request{
		Name:        "before-migration",
		Description: "trying a schema change",
	}, committed)
	hold.NotBefore(newPg, newCfg, nameCheck)

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
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
	assert.Equal(t, committed, savepoint)

	// The store reaches every backend as the DI root left it — there is one, and a
	// backend bound to anything else would be writing outside the project's tree.
	require.Len(t, specs, 2)
	for _, spec := range specs {
		assert.Same(t, store, spec.Store)
	}

	assert.Contains(t, logs.String(), "volume will not be backed up")
	assert.Contains(t, logs.String(), "/scratch")
	assert.Contains(t, logs.String(), "tmpfs")
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
	pgBackend.AssertExpectations(t)
	cfgBackend.AssertExpectations(t)
}

// Labels reach the capture's request as they were asked for.
func TestService_CreateSavepoint_CarriesLabels(t *testing.T) {
	// Arrange
	labels := map[string]string{"team": "payments"}
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	repo := emptySavepointRepo()
	store := newDirStore(t)
	backend := &mockBackend{volume: demoVolume()}
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(backend, nil).Once()
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000", Labels: labels}
	capturer := &mockCapture{}
	expectCapture(capturer, mock.MatchedBy(func(p capture.Plan) bool {
		return sameBackends(p.Volumes, backend)
	}), capture.Request{Labels: labels}, committed)

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{
		Labels: labels,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
	backend.AssertExpectations(t)
}

// The capture runs under the session lock the workflow was granted: the one its commit is verified
// against.
func TestService_CreateSavepoint_CapturesUnderTheSessionLock(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	repo := emptySavepointRepo()
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(&mockBackend{volume: demoVolume()}, nil).Once()
	grantedLock := &mockGrantedSessionLock{}
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	sessionLocker := &mockSessionLocker{}
	sessionLocker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).Return(grantedLock, nil).Once()
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000"}
	capturer := &mockCapture{}
	hold := capturer.On("Hold", mock.Anything, matchLivePlan()).Return([]string(nil), nil).Once()
	captured := capturer.On("Capture", mock.Anything, grantedLock, matchLivePlan(), []string(nil), capture.Request{}).
		Return(committed, nil).Once()
	release := capturer.On("Release", mock.Anything, matchLivePlan()).Return(nil).Once()
	mock.InOrder(hold, captured, release)

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    sessionLocker,
		WriteAdmission:   fakeWriteAdmission{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})

	// Act
	savepoint, err := svc.CreateSavepoint(zerolog.Nop().WithContext(context.Background()), model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
	sessionLocker.AssertExpectations(t)
	capturer.AssertExpectations(t)
}

// savepoint create holds, captures and releases on every path; the hold's or the capture's error
// wins over the release's, and an interrupt wins over the failure it caused.
func TestService_captureHeld(t *testing.T) {
	errBoom := errors.New("boom")
	killed := []string{"c1"}
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000", Name: "sp"}

	tests := []struct {
		name       string
		holdErr    error
		captureErr error
		releaseErr error
		// interruptOn cancels the command's context inside that call: Hold or Capture.
		interruptOn string
		wantErr     string
		// wantLogOrder are lines the log must hold in this order; "release" is the release's.
		wantLogOrder []string
		// wantNoRestartLog says no release failure was logged.
		wantNoRestartLog bool
	}{
		{
			name:             "hold, capture, release",
			wantNoRestartLog: true,
		},
		{
			name:             "a failed hold releases and captures nothing",
			holdErr:          errBoom,
			wantErr:          "boom",
			wantNoRestartLog: true,
		},
		{
			name:        "an interrupted hold releases and captures nothing",
			holdErr:     errBoom,
			interruptOn: "Hold",
			wantErr:     "interrupted: context canceled",
			wantLogOrder: []string{
				"savepoint interrupted while holding the services",
				"release",
				"failure the interrupt caused",
			},
			wantNoRestartLog: true,
		},
		{
			name:             "a failed capture releases",
			captureErr:       errBoom,
			wantErr:          "capturing volumes: boom",
			wantNoRestartLog: true,
		},
		{
			name:        "an interrupted capture releases",
			captureErr:  errBoom,
			interruptOn: "Capture",
			wantErr:     "capturing volumes: interrupted: context canceled",
			wantLogOrder: []string{
				"savepoint interrupted during capture",
				"failure the interrupt caused",
				"release",
			},
			wantNoRestartLog: true,
		},
		{
			name:             "an interrupt past the commit releases and fails nothing",
			interruptOn:      "Commit",
			wantLogOrder:     []string{"savepoint committed before the interrupt", "release"},
			wantNoRestartLog: true,
		},
		{
			name:             "a failed release past the commit names the savepoint",
			releaseErr:       errBoom,
			wantErr:          "savepoint " + committed.ID.String() + " committed; services failed to resume: boom",
			wantNoRestartLog: true,
		},
		{
			name:         "a failed release under a failed capture is logged",
			captureErr:   errors.New("capture boom"),
			releaseErr:   errBoom,
			wantErr:      "capturing volumes: capture boom",
			wantLogOrder: []string{"services failed to resume after capture failure"},
		},
		{
			name:         "a failed release under a failed hold is logged",
			holdErr:      errors.New("stop boom"),
			releaseErr:   errBoom,
			wantErr:      "stop boom",
			wantLogOrder: []string{"services failed to resume after hold failure"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(zerolog.New(logs).WithContext(context.Background()))
			defer cancel()
			interruptIf := func(call string) func(mock.Arguments) {
				return func(mock.Arguments) {
					if tt.interruptOn == call {
						cancel()
					}
				}
			}
			plan := capture.Plan{Project: demoSpec, Spec: demoSpecBytes, WaitTimeout: time.Minute}
			req := capture.Request{Name: "sp"}
			grantedLock := &mockGrantedSessionLock{}

			capturer := &mockCapture{}
			calls := []*mock.Call{
				capturer.On("Hold", mock.Anything, plan).Run(interruptIf("Hold")).Return(killed, tt.holdErr).Once(),
			}
			if tt.holdErr == nil {
				var savepoint model.Savepoint
				if tt.captureErr == nil {
					savepoint = committed
				}
				calls = append(calls, capturer.On("Capture", mock.Anything, grantedLock, plan, killed, req).
					Run(func(args mock.Arguments) {
						interruptIf("Capture")(args)
						interruptIf("Commit")(args)
					}).
					Return(savepoint, tt.captureErr).Once())
			}
			calls = append(calls, capturer.On("Release", mock.Anything, plan).
				Run(func(args mock.Arguments) {
					zerolog.Ctx(args.Get(0).(context.Context)).Info().Msg("release")
				}).
				Return(tt.releaseErr).Once())
			mock.InOrder(calls...)
			svc := New(Deps{Capture: capturer})

			// Act
			savepoint, err := svc.captureHeld(ctx, grantedLock, plan, req)

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, committed, savepoint)
			} else {
				require.EqualError(t, err, tt.wantErr)
				assert.Zero(t, savepoint)
			}
			if tt.holdErr != nil && tt.interruptOn == "" {
				assert.ErrorIs(t, err, tt.holdErr)
			}
			if tt.captureErr != nil && tt.interruptOn == "" {
				assert.ErrorIs(t, err, tt.captureErr)
			}
			assertLogOrder(t, logs.String(), tt.wantLogOrder)
			if tt.wantNoRestartLog {
				assert.NotContains(t, logs.String(), "services failed to resume")
			}
			capturer.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
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
	repo := emptySavepointRepo()

	store := newDirStore(t)
	registry := &mockRegistry{}
	var backends []model.VolumeSnapshotBackend
	for replica := 1; replica <= 3; replica++ {
		objectID := fmt.Sprintf("volume-anon-worker-%d-cache", replica)
		backend := &mockBackend{volume: model.Volume{ObjectID: objectID}}
		registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
			return spec.Store == store && spec.Volume.ObjectID == objectID &&
				spec.Volume.Anon != nil && spec.Volume.Anon.Replica == replica
		})).Return(backend, nil).Once()
		backends = append(backends, backend)
	}
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000"}
	capturer := &mockCapture{}
	expectCapture(capturer, mock.MatchedBy(func(p capture.Plan) bool {
		return sameBackends(p.Volumes, backends...)
	}), capture.Request{}, committed)

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
}

// The backend a new savepoint captures with is the configured one, not a
// constant: it is stamped on every volume the create resolves.
func TestService_CreateSavepoint_StampsTheConfiguredBackend(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	repo := emptySavepointRepo()

	cdcVolume := model.Volume{ObjectID: "volume-named-demo_db", Backend: model.SnapshotBackendCDC}
	backend := &mockBackend{volume: cdcVolume}
	registry := &mockRegistry{}
	registry.On("New", mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Volume.Backend == model.SnapshotBackendCDC
	})).Return(backend, nil).Once()
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000"}
	capturer := &mockCapture{}
	expectCapture(capturer, mock.MatchedBy(func(p capture.Plan) bool {
		return sameBackends(p.Volumes, backend)
	}), capture.Request{}, committed)

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            newDirStore(t),
		CaptureDefaults:  cdcCaptureDefaults(),
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
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
	capturer := &mockCapture{}

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    emptySavepointRepo(),
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  &mockRegistry{},
		Inflight:         &fakeInflight{},
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	// The capture carries no expectations: a run would fail the test where it happened.
	require.ErrorIs(t, err, assembleErr)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	locker.AssertExpectations(t)
	capturer.AssertExpectations(t)
}

// A backend without a factory fails before the services are stopped.
func TestService_CreateSavepoint_UnknownBackend(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	repo := emptySavepointRepo()
	registry := &mockRegistry{}
	registry.On("New", matchSpec(nil, "volume-named-demo_db")).Return(nil, ErrUnknownBackend)
	capturer := &mockCapture{}

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: demoAssembler(),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            nil,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	// The capture carries no expectations: a run would fail the test where it happened.
	require.ErrorIs(t, err, ErrUnknownBackend)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
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
	registry := &mockRegistry{}
	capturer := &mockCapture{}

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    emptySavepointRepo(),
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(empty),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            nil,
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	// Neither the capture nor the registry carries an expectation: a create that
	// covers nothing must not stop the project or build a backend.
	require.ErrorIs(t, err, errNoVolumesToBackUp)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
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
	registry := &mockRegistry{}
	capturer := &mockCapture{}
	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    emptySavepointRepo(),
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        &mockCodec{},
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		CaptureDefaults:  demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrComposeProjectAbsent)
	assert.Contains(t, err.Error(), `compose project "demo"`)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	assembler.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
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
			capturer := &mockCapture{}
			svc := New(Deps{
				ArcaveProject:    demoArcaveProject,
				SessionLocker:    fakeSessionLocker{},
				WriteAdmission:   fakeWriteAdmission{},
				SavepointRepo:    repo,
				ProjectLocker:    locker,
				ComposeInspector: inspector,
				Capture:          capturer,
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
			capturer.AssertExpectations(t)
		})
	}
}

func TestRequireUniqueName(t *testing.T) {
	const id = model.SavepointID("019e843e-1650-7000-8000-000000000000")
	safety := model.Savepoint{ID: id, Name: "pre-rollback", Labels: newSafetySnapshotLabels()}

	tests := []struct {
		name     string
		reqName  string
		existing []model.Savepoint
		wantErr  error
	}{
		{name: "no name", existing: []model.Savepoint{{ID: id}}},
		{name: "a free name", reqName: "v2", existing: []model.Savepoint{{ID: id, Name: "v1"}}},
		{name: "a taken name", reqName: "v1", existing: []model.Savepoint{{ID: id, Name: "v1"}}, wantErr: model.ErrSavepointNameTaken},
		{name: "a safety snapshot's name does not count", reqName: "pre-rollback", existing: []model.Savepoint{safety}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := requireUniqueName(visibleSavepoints(tt.existing), tt.reqName)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
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
	repo := emptySavepointRepo()

	store := newDirStore(t)
	registry := &mockRegistry{}
	backends := make([]model.VolumeSnapshotBackend, 0, 2)
	for _, objectID := range []string{"volume-named-demo_db", "volume-named-demo_orphan"} {
		backend := &mockBackend{volume: model.Volume{ObjectID: objectID}}
		registry.On("New", matchSpec(store, objectID)).Return(backend, nil).Once()
		backends = append(backends, backend)
	}
	committed := model.Savepoint{ID: "019e8488-3ac8-7000-8000-000000000000"}
	capturer := &mockCapture{}
	expectCapture(capturer, mock.MatchedBy(func(p capture.Plan) bool {
		return sameBackends(p.Volumes, backends...) && p.NotCovered.Mounts == nil
	}), capture.Request{}, committed)

	svc := New(Deps{
		ArcaveProject:    demoArcaveProject,
		SessionLocker:    fakeSessionLocker{},
		WriteAdmission:   fakeWriteAdmission{},
		VolumeRemover:    &mockVolumeRemover{},
		SavepointRepo:    repo,
		ProjectLocker:    locker,
		ComposeInspector: inspector,
		ProjectAssembler: assemblerFor(introspection),
		SpecCodec:        demoCodec(),
		Capture:          capturer,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	savepoint, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, committed, savepoint)
	assert.NotContains(t, logs.String(), "volume will not be backed up",
		"nothing is left out for being unreachable any more")
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
	registry.AssertExpectations(t)
	capturer.AssertExpectations(t)
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
			name: "shown with the flag, newest first by default",
			req:  model.ListRequest{IncludeSafetySnapshots: true},
			want: []model.Savepoint{demoSafetySnapshot(), demoRecord()},
		},
		{
			name: "desc",
			req:  model.ListRequest{IncludeSafetySnapshots: true, Sort: model.SortDesc},
			want: []model.Savepoint{demoSafetySnapshot(), demoRecord()},
		},
		{
			name: "asc keeps the repository's order",
			req:  model.ListRequest{IncludeSafetySnapshots: true, Sort: model.SortAsc},
			want: []model.Savepoint{demoRecord(), demoSafetySnapshot()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepointRepo{}
			repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{demoRecord(), demoSafetySnapshot()}, nil).Once()
			// No daemon collaborator at all: savepoint list runs where no daemon does.
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				SessionLocker: fakeSessionLocker{},
				SavepointRepo: repo, Inflight: &fakeInflight{},
				CaptureDefaults: demoCaptureDefaults,
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
// see it, and that is how one is removed by hand.
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
			req:  model.RemoveRequest{RetainRecent: utils.New(1), DryRun: true},
			want: nil,
		},
		{
			// Deleting every savepoint still leaves the undo: the scan never sees it.
			name: "retain-recent 0 takes the savepoint and leaves the backup alone",
			req:  model.RemoveRequest{RetainRecent: utils.New(0), DryRun: true},
			want: []model.Savepoint{record},
		},
		{
			name: "an age rule sweeps neither",
			req:  model.RemoveRequest{BeforeDate: "2026-06-12T11:30:00Z", DryRun: true},
			want: []model.Savepoint{record},
		},
		{
			name: "an explicit id reaches the backup",
			req:  model.RemoveRequest{Refs: []string{backup.ID.String()}, DryRun: true},
			want: []model.Savepoint{backup},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepointRepo{}
			repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record, backup}, nil).Once()
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				SessionLocker: fakeSessionLocker{},
				VolumeRemover: &mockVolumeRemover{},
				SavepointRepo: repo, ProjectLocker: nil, ComposeInspector: nil,
				ComposeRunner: nil, BackendRegistry: nil, Inflight: &fakeInflight{},
				Store: nil, CaptureDefaults: demoCaptureDefaults,
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

// Removal deletes the savepoint it selects through the repository, which owns the order.
func TestService_RemoveSavepoints(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("DeleteSavepoint", mock.Anything, record.ID).Return(nil).Once()

	// No daemon collaborator at all: savepoint remove runs where no daemon does.
	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		SavepointRepo:  repo, ProjectLocker: locker,
		Inflight: &fakeInflight{}, CaptureDefaults: demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A savepoint the repository fails to delete may be left uncommitted with its tree behind it; the
// orphan is named in the log.
func TestService_RemoveSavepoints_DeleteFailureNamesTheOrphan(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("DeleteSavepoint", mock.Anything, record.ID).Return(errors.New("remove boom")).Once()

	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		VolumeRemover:  &mockVolumeRemover{},
		SavepointRepo:  repo, ProjectLocker: locker, ComposeInspector: nil,
		ComposeRunner: nil, Inflight: &fakeInflight{},
		CaptureDefaults: demoCaptureDefaults,
	})
	logs := &syncBuffer{}
	ctx := zerolog.New(logs).WithContext(context.Background())

	// Act
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove boom")
	assert.Contains(t, logs.String(), "the directory is orphaned")
	assert.Contains(t, logs.String(), record.ID.String())
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A dry run takes no lock and deletes nothing.
func TestService_RemoveSavepoints_DryRun(t *testing.T) {
	// Arrange
	record := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "sp", CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC)}
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil)
	locker := &mockLocker{}

	svc := New(Deps{
		ArcaveProject: demoArcaveProject,
		SessionLocker: fakeSessionLocker{},
		VolumeRemover: &mockVolumeRemover{},
		SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: nil,
		ComposeRunner: nil, Inflight: &fakeInflight{},
		Store: nil, CaptureDefaults: demoCaptureDefaults,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}, DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	// Only List is expected: a Delete or an Acquire would fail the run where it happened.
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// TestValidateRemoveRequest covers the selector rules. A negative --retain-recent
// must be reported as such, not as a missing selector.
func TestValidateRemoveRequest(t *testing.T) {
	tests := []struct {
		name string
		req  model.RemoveRequest
		want error
	}{
		{name: "explicit ids", req: model.RemoveRequest{Refs: []string{"1"}}},
		{name: "retain recent", req: model.RemoveRequest{RetainRecent: utils.New(2)}},
		{name: "retain recent zero is a rule", req: model.RemoveRequest{RetainRecent: utils.New(0)}},
		{name: "retain for", req: model.RemoveRequest{RetainFor: "1d"}},
		{
			name: "ids and retention conflict",
			req:  model.RemoveRequest{Refs: []string{"1"}, RetainRecent: utils.New(2)},
			want: model.ErrRemoveSelectorConflict,
		},
		{
			name: "ids and retain recent zero conflict",
			req:  model.RemoveRequest{Refs: []string{"1"}, RetainRecent: utils.New(0)},
			want: model.ErrRemoveSelectorConflict,
		},
		{name: "no selector at all", req: model.RemoveRequest{}, want: model.ErrRemoveNoSelector},
		{
			name: "negative retain recent",
			req:  model.RemoveRequest{RetainRecent: utils.New(-1)},
			want: model.ErrRetainRecentNegative,
		},
		{
			name: "negative retain recent beats a conflicting id list",
			req:  model.RemoveRequest{Refs: []string{"1"}, RetainRecent: utils.New(-1)},
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

// TestService_parseRetentionCutoffs pins the two rules against each other: --retain-for
// counts back from the wall clock, --before-date is taken verbatim, and both
// together keep that order. A relative cutoff is checked against the clock the
// call itself read, so the tolerance is the test's own runtime.
func TestService_parseRetentionCutoffs(t *testing.T) {
	beforeDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		req       model.RemoveRequest
		want      []time.Duration // a cutoff relative to now; zero with an entry in wantAbs means "read it from wantAbs"
		wantAbs   map[int]time.Time
		wantErr   bool
		wantErrIs error
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
		{
			name:      "negative retain-for is refused",
			req:       model.RemoveRequest{RetainFor: "-1h"},
			wantErr:   true,
			wantErrIs: model.ErrRetainForNegative,
		},
		{
			name: "zero retain-for cuts at now",
			req:  model.RemoveRequest{RetainFor: "0s"},
			want: []time.Duration{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				SessionLocker: fakeSessionLocker{},
				VolumeRemover: &mockVolumeRemover{},
				SavepointRepo: nil, ProjectLocker: nil, ComposeInspector: nil,
				ComposeRunner: nil, BackendRegistry: nil, Inflight: &fakeInflight{},
				Store: nil, CaptureDefaults: demoCaptureDefaults,
			})
			now := time.Now()

			// Act
			got, err := svc.parseRetentionCutoffs(tt.req)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					require.ErrorIs(t, err, tt.wantErrIs)
				}
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
		ID: "400", Name: "dup", Labels: newSafetySnapshotLabels(), CreatedAt: now.Add(3 * time.Hour),
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
		{ID: "400", Name: name, Labels: newSafetySnapshotLabels(), CreatedAt: now},
	}

	// Act
	_, err := findSavepoint(records, model.RollbackRequest{Name: &name})

	// Assert
	var ambiguous *model.AmbiguousSavepointNameError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, name, ambiguous.Name)
	assert.Equal(t, []model.SavepointID{"200", "300"}, ambiguous.IDs, "sorted, and never the safety snapshot")
	assert.EqualError(t, err, `2 savepoints are named "dup"; name one by id instead: 200, 300`)
}

// TestPartitionByRefs pins what a remove ref reaches: an id every record, a name
// only the visible ones; a ref naming nothing or several fails the whole request.
func TestPartitionByRefs(t *testing.T) {
	record, backup := demoRecord(), demoSafetySnapshot()
	other := model.Savepoint{
		ID:        "019e8488-3ac8-7000-8000-000000000000",
		Name:      "other",
		CreatedAt: record.CreatedAt,
	}
	records := []model.Savepoint{record, backup, other}

	tests := []struct {
		name        string
		refs        []string
		wantKept    []model.Savepoint
		wantRemoved []model.Savepoint
		wantErr     string
	}{
		{
			name:        "by id",
			refs:        []string{record.ID.String()},
			wantKept:    []model.Savepoint{backup, other},
			wantRemoved: []model.Savepoint{record},
		},
		{
			name:        "by name",
			refs:        []string{"other"},
			wantKept:    []model.Savepoint{record, backup},
			wantRemoved: []model.Savepoint{other},
		},
		{
			name:        "an id and a name mixed",
			refs:        []string{"sp", other.ID.String()},
			wantKept:    []model.Savepoint{backup},
			wantRemoved: []model.Savepoint{record, other},
		},
		{
			name:        "one savepoint named twice is removed once",
			refs:        []string{"sp", record.ID.String()},
			wantKept:    []model.Savepoint{backup, other},
			wantRemoved: []model.Savepoint{record},
		},
		{
			name:        "an id reaches a safety snapshot",
			refs:        []string{backup.ID.String()},
			wantKept:    []model.Savepoint{record, other},
			wantRemoved: []model.Savepoint{backup},
		},
		{
			name:    "a name does not reach a safety snapshot",
			refs:    []string{backup.Name},
			wantErr: "savepoint(s) not found: " + backup.Name,
		},
		{
			name:    "every ref naming nothing is reported",
			refs:    []string{"nope", "sp", "019e8497-cf10-7000-8000-000000000000"},
			wantErr: "savepoint(s) not found: nope, 019e8497-cf10-7000-8000-000000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			kept, removed, err := partitionByRefs(records, tt.refs)

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, kept)
				assert.Nil(t, removed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKept, kept)
			assert.Equal(t, tt.wantRemoved, removed)
		})
	}
}

func TestPartitionByRefs_AmbiguousName(t *testing.T) {
	// Arrange
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	records := []model.Savepoint{
		{ID: "019e8488-3ac8-7000-8000-000000000000", Name: "dup", CreatedAt: now},
		{ID: "019e843e-1650-7000-8000-000000000000", Name: "dup", CreatedAt: now},
	}

	// Act
	kept, removed, err := partitionByRefs(records, []string{"dup"})

	// Assert
	var ambiguous *model.AmbiguousSavepointNameError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, []model.SavepointID{records[1].ID, records[0].ID}, ambiguous.IDs)
	assert.Nil(t, kept)
	assert.Nil(t, removed)
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
// only has to be unique per volume.
type fakeVolumeStreamer struct {
	mu      sync.Mutex
	payload string
	written strings.Builder
}

func (f *fakeVolumeStreamer) ResolveMountSource(
	_ context.Context, v model.Volume,
) (model.MountSource, error) {
	return model.MountSource{Type: model.MountSourceVolume, Source: v.ObjectID}, nil
}

func (f *fakeVolumeStreamer) OpenVolume(
	_ context.Context, _ model.MountSource,
) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(strings.NewReader(f.payload)), nil
}

func (f *fakeVolumeStreamer) WriteVolume(_ context.Context, _ model.MountSource, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := io.Copy(&f.written, r)
	return err
}

// sameBackends reports whether got holds exactly want, by identity and in any order: the
// plan carries the very backends the registry built.
func sameBackends(got []model.VolumeSnapshotBackend, want ...model.VolumeSnapshotBackend) bool {
	if len(got) != len(want) {
		return false
	}
	for _, backend := range want {
		if !slices.Contains(got, backend) {
			return false
		}
	}
	return true
}
