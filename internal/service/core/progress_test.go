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
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/capture"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// progressRecorder renders every step's last event as "<path> <status>[ error=…]", in the order
// the steps end; the tracker calls it under its own lock.
type progressRecorder struct {
	paths map[progress.StepID]string
	ended []string
}

func (r *progressRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		segment := string(evt.Open.Step.Kind)
		if id := evt.Open.Step.ID; id != "" {
			segment += "[" + id + "]"
		}
		if parent := r.paths[evt.Open.Parent]; parent != "" {
			segment = parent + "/" + segment
		}
		r.paths[evt.StepID] = segment
	}
	if !evt.Status.IsTerminal() {
		return
	}
	line := r.paths[evt.StepID] + " " + string(evt.Status)
	if evt.Error != "" {
		line += " error=" + evt.Error
	}
	r.ended = append(r.ended, line)
}

func newProgressContext() (context.Context, *progressRecorder) {
	recorder := &progressRecorder{paths: map[progress.StepID]string{}}
	ctx := zerolog.Nop().WithContext(context.Background())
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	return progress.WithTracker(ctx, progress.New(recorder, now)), recorder
}

// captureDoubles are the doubles a newCaptureService runs on.
type captureDoubles struct {
	inspector *mockInspector
	locker    *mockLocker
	runner    *mockComposeRunner
	repo      *mockSavepointRepo
	images    *mockImageBackend
	registry  *mockRegistry
	backend   *mockBackend
}

// newCaptureService is a service over the real capture whose one volume's snapshot returns
// snapshotErr.
func newCaptureService(t *testing.T, snapshotErr error) (*Service, captureDoubles) {
	t.Helper()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil)
	locker := &mockLocker{}
	expectLock(locker)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once()
	runner.On("Start", mock.Anything, demoSpec, time.Duration(0)).Return(nil).Once()
	repo := emptySavepointRepo()
	backend := &mockBackend{volume: demoVolume()}
	if snapshotErr == nil {
		expectSpecCommit(repo)
		repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(r model.Savepoint) bool {
			return r.ID.Validate() == nil && len(r.Stats) == 1 &&
				r.Stats[0].Volume.ObjectID == "volume-named-demo_db" && r.Stats[0].SizeBytes == 42
		})).Return(nil).Once()
		backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
			Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy, SizeBytes: 42}, nil).Once()
	} else {
		backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
			Return(model.SnapshotStat{}, snapshotErr).Once()
	}
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(backend, nil).Once()
	images := &mockImageBackend{}

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
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
		Capture: capture.New(capture.Deps{
			SavepointRepo: repo,
			ImageBackend:  images,
			ComposeRunner: runner,
			Store:         store,
		}),
	})
	return svc, captureDoubles{
		inspector: inspector,
		locker:    locker,
		runner:    runner,
		repo:      repo,
		images:    images,
		registry:  registry,
		backend:   backend,
	}
}

func TestService_CreateSavepointProgress(t *testing.T) {
	errDisk := errors.New("disk full")
	tests := []struct {
		name        string
		snapshotErr error
		want        []string
	}{
		{
			name: "every step ends where the workflow opened it",
			want: []string{
				"savepoint.create/project.introspect succeeded",
				"savepoint.create/compose.stop succeeded",
				"savepoint.create/savepoint.capture/volume.capture/volume[volume-named-demo_db] succeeded",
				"savepoint.create/savepoint.capture/volume.capture succeeded",
				"savepoint.create/savepoint.capture succeeded",
				"savepoint.create/compose.start succeeded",
				"savepoint.create succeeded",
			},
		},
		{
			name:        "a failed capture carries its error on the volume alone",
			snapshotErr: errDisk,
			want: []string{
				"savepoint.create/project.introspect succeeded",
				"savepoint.create/compose.stop succeeded",
				"savepoint.create/savepoint.capture/volume.capture/volume[volume-named-demo_db] failed " +
					"error=snapshot volume-named-demo_db: capture volume: disk full",
				"savepoint.create/savepoint.capture/volume.capture failed",
				"savepoint.create/savepoint.capture failed",
				"savepoint.create/compose.start succeeded",
				"savepoint.create failed",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, recorder := newProgressContext()
			svc, doubles := newCaptureService(t, tt.snapshotErr)

			// Act
			_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

			// Assert
			if tt.snapshotErr != nil {
				require.ErrorIs(t, err, tt.snapshotErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, recorder.ended)
			doubles.inspector.AssertExpectations(t)
			doubles.locker.AssertExpectations(t)
			doubles.runner.AssertExpectations(t)
			doubles.repo.AssertExpectations(t)
			doubles.images.AssertExpectations(t)
			doubles.registry.AssertExpectations(t)
			doubles.backend.AssertExpectations(t)
		})
	}
}

// A rollback's safety snapshot is captured under its own step: the capture's steps open under
// rollback.safety-snapshot, by the same code savepoint create runs them with.
func TestService_Rollback_Progress(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	vol := demoVolume()
	record := demoRecord()
	repo, inspector, locker := demoResolution(record)
	runner := &mockComposeRunner{}
	safetySnapshotBackend := &mockBackend{volume: vol}
	savepointBackend := &mockBackend{volume: vol}
	mock.InOrder(
		runner.On("Stop", mock.Anything, demoSpec).Return([]string(nil), nil).Once(),
		safetySnapshotBackend.On("Snapshot", mock.Anything, matchSafetySnapshot()).
			Return(model.SnapshotStat{Volume: vol, Type: model.SnapshotBackendFullCopy}, nil).Once(),
		expectSpecCommit(repo),
		expectSafetySnapshotCommit(repo, record),
		runner.On("Down", mock.Anything, demoSpec).Return(nil).Once(),
		runner.On("Create", mock.Anything, rolledBackTo(demoTargetSpec, record), demoRestoredVolumes).
			Return(nil).Once(),
		savepointBackend.On("Restore", mock.Anything, model.BackendRestoreRequest{
			SnapshotID: record.ID,
			Payload:    "sp-payload",
			SizeBytes:  demoVolumeSize,
		}).Return(nil).Once(),
		runner.On("Start", mock.Anything, deployed(demoTargetSpec), 60*time.Second).Return(nil).Once(),
	)
	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(safetySnapshotBackend, nil).Once()
	registry.On("New", matchSpec(store, vol.ObjectID)).Return(savepointBackend, nil).Once()
	images := &mockImageBackend{}
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
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Inflight:         &fakeInflight{},
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
		Capture: capture.New(capture.Deps{
			SavepointRepo: repo,
			ImageBackend:  images,
			ComposeRunner: runner,
			Store:         store,
		}),
	})

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID:          ptrID(record.ID),
		WaitTimeout: 60 * time.Second,
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"rollback/project.introspect succeeded",
		"rollback/rollback.safety-snapshot/compose.stop succeeded",
		"rollback/rollback.safety-snapshot/savepoint.capture/volume.capture/volume[volume-named-demo_db] succeeded",
		"rollback/rollback.safety-snapshot/savepoint.capture/volume.capture succeeded",
		"rollback/rollback.safety-snapshot/savepoint.capture succeeded",
		"rollback/rollback.safety-snapshot succeeded",
		"rollback/rollback.apply/compose.down succeeded",
		"rollback/rollback.apply/compose.create succeeded",
		"rollback/rollback.apply/volume.restore/volume[volume-named-demo_db] succeeded",
		"rollback/rollback.apply/volume.restore succeeded",
		"rollback/rollback.apply/compose.start succeeded",
		"rollback/rollback.apply succeeded",
		"rollback/rollback.cleanup succeeded",
		"rollback succeeded",
	}, recorder.ended)
	repo.AssertExpectations(t)
	inspector.AssertExpectations(t)
	locker.AssertExpectations(t)
	runner.AssertExpectations(t)
	images.AssertExpectations(t)
	registry.AssertExpectations(t)
	safetySnapshotBackend.AssertExpectations(t)
	savepointBackend.AssertExpectations(t)
}

// A remove reports the savepoints it drops and no volume: it reaches no backend.
func TestService_RemoveSavepoints_Progress(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepointRepo{}
	repo.On("ListSavepoints", mock.Anything).Return([]model.Savepoint{record}, nil).Once()
	repo.On("DeleteSavepoint", mock.Anything, record.ID).Return(nil).Once()
	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		ProjectLocker:  locker,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		SavepointRepo:  repo,
		Inflight:       &fakeInflight{},
	})

	// Act
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"savepoint.remove/savepoint.drop/savepoint[" + record.ID.String() + "] succeeded",
		"savepoint.remove/savepoint.drop succeeded",
		"savepoint.remove succeeded",
	}, recorder.ended)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A lost lock cancels the work under it, and the root, opened outside the lock, carries why.
func TestWithSessionLock_LostLockCancelsTheWork(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	locker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{lostCause: model.ErrSessionLockLost}
	locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).Return(grantedLock, nil).Once()
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	ctx, root := progress.Begin(ctx, progress.Step{Kind: kindSavepointCreate})

	// Act
	_, err := withSessionLock(ctx, locker, model.SessionLockOperationSavepointCreate,
		func(ctx context.Context, _ model.GrantedSessionLock) (string, error) {
			_, step := progress.Run(ctx, progress.Step{Kind: kindComposeDown})
			err := preferInterrupt(ctx, errWorkflow)
			step.End(err)
			return "", err
		})
	root.End(err)

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	require.Len(t, recorder.ended, 2)
	assert.Equal(t, "savepoint.create/compose.down cancelled", recorder.ended[0])
	assert.True(t, strings.HasPrefix(recorder.ended[1], "savepoint.create failed error=interrupted: "),
		"the cause is on the root: %s", recorder.ended[1])
	assert.Contains(t, recorder.ended[1], model.ErrSessionLockLost.Error())
	locker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// The lock's step is shown only once the locker waits on another holder.
func TestWithSessionLock_ShowsTheWaitOnlyWhenHeld(t *testing.T) {
	tests := []struct {
		name string
		held bool
		want []string
	}{
		{
			name: "a free lock shows nothing, so the lazy root never opens",
		},
		{
			name: "a held lock shows the wait",
			held: true,
			want: []string{"savepoint.create/session.lock succeeded", "savepoint.create succeeded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, recorder := newProgressContext()
			locker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
				Run(func(args mock.Arguments) {
					if tt.held {
						progress.Current(args.Get(0).(context.Context)).Warn("waiting: held by rollback")
					}
				}).
				Return(grantedLock, nil).Once()
			grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
			ctx, root := progress.Begin(ctx, progress.Step{Kind: kindSavepointCreate})

			// Act
			_, err := withSessionLock(ctx, locker, model.SessionLockOperationSavepointCreate,
				func(context.Context, model.GrantedSessionLock) (string, error) { return "", nil })
			root.End(err)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, recorder.ended)
			locker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}

// warnedRecorder keeps every warning, and whether it was marked as the result repeats it.
type warnedRecorder struct {
	msgs     []string
	inResult []bool
}

func (r *warnedRecorder) Render(evt progress.Event) {
	if evt.Status == progress.StatusWarned {
		r.msgs = append(r.msgs, evt.Msg)
		r.inResult = append(r.inResult, evt.InResult)
	}
}
