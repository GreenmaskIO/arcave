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
	"github.com/greenmaskio/arcave/pkg/progress"
)

// progressRecorder renders every node's last event as "<path> <status>[ error=…]", in the order
// the nodes end; the tracker calls it under its own lock.
type progressRecorder struct {
	paths map[progress.NodeID]string
	ended []string
}

func (r *progressRecorder) Report(evt progress.Event) {
	if evt.Open != nil {
		segment := string(evt.Open.Step.Kind)
		if id := evt.Open.Step.ID; id != "" {
			segment += "[" + id + "]"
		}
		if parent := r.paths[evt.Open.Parent]; parent != "" {
			segment = parent + "/" + segment
		}
		r.paths[evt.Node] = segment
	}
	if !evt.Status.IsTerminal() {
		return
	}
	line := r.paths[evt.Node] + " " + string(evt.Status)
	if evt.Error != "" {
		line += " error=" + evt.Error
	}
	r.ended = append(r.ended, line)
}

func newProgressContext() (context.Context, *progressRecorder) {
	recorder := &progressRecorder{paths: map[progress.NodeID]string{}}
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
	registry  *mockRegistry
	backend   *mockBackend
}

// newCaptureService is a service whose one volume's snapshot returns snapshotErr.
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

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
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
	})
	return svc, captureDoubles{
		inspector: inspector,
		locker:    locker,
		runner:    runner,
		repo:      repo,
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
			doubles.registry.AssertExpectations(t)
			doubles.backend.AssertExpectations(t)
		})
	}
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
			_, node := progress.Run(ctx, progress.Step{Kind: kindComposeStop})
			err := preferInterrupt(ctx, errWorkflow)
			node.End(err)
			return "", err
		})
	root.End(err)

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	require.Len(t, recorder.ended, 2)
	assert.Equal(t, "savepoint.create/compose.stop cancelled", recorder.ended[0])
	assert.True(t, strings.HasPrefix(recorder.ended[1], "savepoint.create failed error=interrupted: "),
		"the cause is on the root: %s", recorder.ended[1])
	assert.Contains(t, recorder.ended[1], model.ErrSessionLockLost.Error())
	locker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// The lock's node is shown only once the locker waits on another holder.
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

func TestNewNotCoveredWarning(t *testing.T) {
	tests := []struct {
		name       string
		notCovered *model.NotCovered
		want       string
	}{
		{
			name: "nothing left out",
		},
		{
			name:       "an empty report",
			notCovered: &model.NotCovered{},
		},
		{
			name: "every kind left out is counted",
			notCovered: &model.NotCovered{
				Mounts:           []model.SkippedVolume{{}, {}},
				KilledContainers: []model.KilledContainer{{}},
			},
			want: "not covered: 2 mount(s), 1 killed container(s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newNotCoveredWarning(tt.notCovered)

			assert.Equal(t, tt.want, got)
		})
	}
}

// warnedRecorder keeps every warning, and whether it was marked as the result repeats it.
type warnedRecorder struct {
	msgs     []string
	inResult []bool
}

func (r *warnedRecorder) Report(evt progress.Event) {
	if evt.Status == progress.StatusWarned {
		r.msgs = append(r.msgs, evt.Msg)
		r.inResult = append(r.inResult, evt.InResult)
	}
}

// A capture's warnings are marked InResult, a safety snapshot's are not.
func TestWarn(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   []bool
	}{
		{
			name: "an ordinary savepoint is the result",
			want: []bool{true},
		},
		{
			name:   "a safety snapshot is not",
			labels: newSafetySnapshotLabels(),
			want:   []bool{false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				recorder = &warnedRecorder{}
				now      = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
				ctx      = zerolog.Nop().WithContext(context.Background())
			)
			_, node := progress.New(recorder, now).Begin(ctx, progress.Step{Kind: kindSavepointCapture})

			warn(node, "not covered: 1 mount(s)", isInResult(tt.labels))

			assert.Equal(t, tt.want, recorder.inResult)
		})
	}
}

func TestNewImageSample(t *testing.T) {
	tests := []struct {
		name  string
		image model.Image
		want  progress.Sample
	}{
		{
			name: "stored by this capture",
			image: model.Image{
				SizeBytes:   100,
				StoredBytes: 40,
				Stored:      true,
			},
			want: newCaptureSample(100, 40, 60),
		},
		{
			name: "already stored",
			image: model.Image{
				SizeBytes: 100,
				Stored:    true,
			},
			want: newCaptureSample(100, 0, 100),
		},
		{
			name: "not stored moves nothing",
			image: model.Image{
				SizeBytes: 100,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newImageSample(tt.image)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
