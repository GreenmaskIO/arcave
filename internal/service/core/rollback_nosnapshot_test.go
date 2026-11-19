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
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/core/internal/workflow"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// A request skipping the safety snapshot, or a recovery, plans and holds nothing: it warns, writes
// its marker — restoring on a recovery — tears the live workload down and builds the target.
func TestService_Rollback_WithoutSafetySnapshot(t *testing.T) {
	recovered := newRecoveredMarker(demoRecord().ID, model.InflightRestoring, true)
	inconsistent := newRecoveredMarker(demoRecord().ID, model.InflightInconsistent, true)
	rerunRecreating := newRecoveredMarker(demoRecord().ID, model.InflightRecreating, false)
	rerunDone := newRecoveredMarker(demoRecord().ID, model.InflightRestoreDone, false)
	another := newRecoveredMarker("019e83ef-ee80-7000-8000-000000000000", model.InflightRestoring, false)
	tests := []struct {
		name       string
		standing   *model.InflightMarker
		skip       bool
		wantPhase  model.InflightPhase
		wantSafety model.SavepointID
	}{
		{name: "skipped by the request", skip: true, wantPhase: model.InflightRecreating},
		{
			name:       "a recovery to the safety snapshot",
			standing:   &recovered,
			wantPhase:  model.InflightRestoring,
			wantSafety: demoRecord().ID,
		},
		{
			name:       "a recovery to the safety snapshot past a failed revert",
			standing:   &inconsistent,
			wantPhase:  model.InflightRestoring,
			wantSafety: demoRecord().ID,
		},
		{name: "a recovery of the same rollback before a write", standing: &rerunRecreating, wantPhase: model.InflightRestoring},
		{name: "a recovery of the same rollback past the restore", standing: &rerunDone, wantPhase: model.InflightRestoring},
		// A savepoint that will not come up holds nothing hostage: another one recovers over a
		// running project too, taking no snapshot of its half-restored state.
		{name: "a recovery to another savepoint", standing: &another, wantPhase: model.InflightRestoring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(false)
			stages.target.prepared.summary = demoSummary()
			recovering := tt.standing != nil
			inflight := &mockInflight{}
			getStanding := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
			if recovering {
				getStanding.Return(*tt.standing, true, nil)
			}
			mock.InOrder(
				getStanding,
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, recovering), nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, tt.wantPhase, tt.wantSafety)).Return(nil).Once(),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoring, tt.wantSafety)).Return(nil).Once(),
				stages.target.expectRestoreData(nil),
				inflight.On("Put", mock.Anything, isInflightMarkerOf(record.ID, model.InflightRestoreDone, tt.wantSafety)).Return(nil).Once(),
				stages.target.expectStart(60*time.Second, nil),
				stages.target.expectReap(),
				inflight.On("Clear", mock.Anything).Return(nil).Once(),
			)
			svc := newStagedService(t, stages, savepoints, locker, inflight)
			recorder := &warnRecorder{}
			ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, fixedNow))

			// Act
			res, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: tt.skip,
			})

			// Assert
			require.NoError(t, err)
			assert.Empty(t, res.SafetySnapshot)
			assert.Equal(t, demoSummary().Volumes, res.Restored)
			assert.Equal(t, []string{noSafetySnapshotWarning}, recorder.inResult)
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// Without a safety snapshot nothing is reverted: a failure past the marker leaves it standing,
// nothing reaped; a marker that cannot be written touches nothing; an interrupt during the prepare
// writes no marker.
func TestService_Rollback_WithoutSafetySnapshotFailures(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name        string
		interrupt   bool
		putErr      error
		teardownErr error
		wantErr     string
	}{
		{name: "a failure past the marker", teardownErr: errBoom, wantErr: "rollback failed: boom"},
		{
			name:    "a marker that cannot be written",
			putErr:  errors.New("disk full"),
			wantErr: "recording the inflight marker, nothing changed: disk full",
		},
		{
			name:      "an interrupt during the prepare",
			interrupt: true,
			wantErr:   "rollback interrupted, nothing changed: context canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
			defer cancel()
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(false)
			inflight := &mockInflight{}
			prepare := stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil)
			calls := []*mock.Call{
				inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
				stages.expectInspect(nil),
				prepare,
			}
			if tt.interrupt {
				prepare.Run(func(mock.Arguments) { cancel() })
			} else {
				calls = append(calls, inflight.On("Put", mock.Anything,
					isInflightMarker(record.ID, model.InflightRecreating)).Return(tt.putErr).Once())
			}
			if tt.teardownErr != nil {
				calls = append(calls, stages.expectLiveTeardown(tt.teardownErr))
			}
			mock.InOrder(calls...)
			svc := newStagedService(t, stages, savepoints, locker, inflight)

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{
				ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
			})

			// Assert
			require.EqualError(t, err, tt.wantErr)
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A compose project with no containers is built from the savepoint: no hold, no safety snapshot,
// the marker naming none. What the prepared savepoint overwrites is the result's leftover volumes
// and a warning the result repeats; a recovery's are only logged.
func TestService_Rollback_ColdStartBuildsFromTheSavepoint(t *testing.T) {
	recovered := newRecoveredMarker(demoRecord().ID, model.InflightRestoring, false)
	tests := []struct {
		name         string
		standing     *model.InflightMarker
		wantLeftover []string
		wantPhase    model.InflightPhase
	}{
		{name: "a leftover volume overwritten", wantLeftover: []string{"demo_db"}, wantPhase: model.InflightRecreating},
		{name: "a recovery names none", standing: &recovered, wantPhase: model.InflightRestoring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(true)
			stages.target.prepared.summary = provider.PreparedSummary{
				Volumes:     []model.Volume{demoVolume()},
				Overwritten: []string{"demo_db"},
			}
			recovering := tt.standing != nil
			inflight := &mockInflight{}
			getStanding := inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
			if recovering {
				getStanding.Return(*tt.standing, true, nil)
			}
			mock.InOrder(
				getStanding,
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, recovering), nil),
				inflight.On("Put", mock.Anything, isInflightMarker(record.ID, tt.wantPhase)).Return(nil).Once(),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(nil),
				inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoring)).Return(nil).Once(),
				stages.target.expectRestoreData(nil),
				inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoreDone)).Return(nil).Once(),
				stages.target.expectStart(60*time.Second, nil),
				stages.target.expectReap(),
				inflight.On("Clear", mock.Anything).Return(nil).Once(),
			)
			svc := newStagedService(t, stages, savepoints, locker, inflight)
			recorder := &warnRecorder{}
			logs := &syncBuffer{}
			ctx := progress.WithTracker(zerolog.New(logs).WithContext(context.Background()), progress.New(recorder, fixedNow))

			// Act
			res, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.NoError(t, err)
			assert.Empty(t, res.SafetySnapshot)
			assert.Equal(t, tt.wantLeftover, res.LeftoverVolumes)
			if recovering {
				assert.Empty(t, recorder.inResult)
				assert.Contains(t, logs.String(), "the compose project is not running but this volume survives")
			} else {
				assert.Equal(t, []string{model.NewLeftoverVolumeNotice("demo_db").Message}, recorder.inResult)
			}
			inflight.AssertExpectations(t)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A cold start the prepare refuses — a leftover volume under the refusing policy among the
// reasons — writes no marker and builds nothing.
func TestService_Rollback_ColdStartRefusedByThePrepare(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(true)
	stages.expectInspect(nil)
	stages.expectPrepare(record.ID, demoPrepareOptions(record, false), model.ErrLeftoverVolumes)
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	svc := newStagedService(t, stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Rollback(zerolog.Nop().WithContext(context.Background()),
		model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: time.Second})

	// Assert
	require.ErrorIs(t, err, model.ErrLeftoverVolumes)
	inflight.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// An interrupt that lands while the savepoint is prepared stops a cold start before the marker:
// nothing is written or created.
func TestService_Rollback_ColdStartInterruptBeforeTheMarker(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	stages := newMockStages(true)
	inflight := &mockInflight{}
	mock.InOrder(
		inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
		stages.expectInspect(nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil).Run(func(mock.Arguments) { cancel() }),
	)
	svc := newStagedService(t, stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: time.Second})

	// Assert
	require.EqualError(t, err, "rollback interrupted, nothing changed: context canceled")
	inflight.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A cold start that fails discards what it built; the marker is cleared while no volume was
// written, and kept at restoring once one may have been — restore-done going back, since what the
// restore wrote into has just gone.
func TestService_Rollback_ColdStartFailure(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name          string
		teardownErr   error
		buildErr      error
		restoreErr    error
		startErr      error
		discardErr    error
		wantErr       string
		wantMarker    bool
		wantMarkPhase model.InflightPhase
		wantLog       string
	}{
		{name: "a failed teardown clears the marker, nothing to discard", teardownErr: errBoom, wantErr: "rollback failed: boom"},
		{name: "a failed build clears the marker", buildErr: errBoom, wantErr: "rollback failed: boom"},
		{
			name:          "a failed restore keeps the marker",
			restoreErr:    errBoom,
			wantErr:       "rollback failed: boom",
			wantMarker:    true,
			wantMarkPhase: model.InflightRestoring,
		},
		{
			name:          "a failed start keeps the marker, not at restore-done",
			startErr:      errBoom,
			wantErr:       "rollback failed: services did not become healthy after restore: boom",
			wantMarker:    true,
			wantMarkPhase: model.InflightRestoring,
		},
		{
			name:       "a failed discard is logged",
			buildErr:   errBoom,
			discardErr: errors.New("down boom"),
			wantErr:    "rollback failed: boom",
			wantLog:    "failed to remove what the failed rollback created",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(true)
			calls := []*mock.Call{
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
				stages.expectLiveTeardown(tt.teardownErr),
			}
			if tt.teardownErr == nil {
				calls = append(calls, stages.target.expectBuild(tt.buildErr))
			}
			if tt.teardownErr == nil && tt.buildErr == nil {
				calls = append(calls, stages.target.expectRestoreData(tt.restoreErr))
			}
			if tt.startErr != nil {
				calls = append(calls, stages.target.expectStart(60*time.Second, tt.startErr))
			}
			if tt.teardownErr == nil {
				calls = append(calls, stages.target.expectDiscard(tt.discardErr))
			}
			mock.InOrder(calls...)
			inflight := &fakeInflight{}
			svc := newStagedService(t, stages, savepoints, locker, inflight)
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert
			require.EqualError(t, err, tt.wantErr)
			marker, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			assert.Equal(t, tt.wantMarker, marked)
			if tt.wantMarker {
				assert.Equal(t, tt.wantMarkPhase, marker.Phase)
				assert.Equal(t, record.ID, marker.Target)
				assert.Empty(t, marker.Safety)
			}
			assert.Contains(t, logs.String(), tt.wantLog)
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// Recovering from an interrupted rollback with no containers left prepares as a recovery, warns of
// no leftover volume, and its marker never goes back to "nothing written": a failure keeps it.
func TestService_Rollback_ColdStartRerun(t *testing.T) {
	tests := []struct {
		name     string
		phase    model.InflightPhase
		toSafety bool
	}{
		{name: "past a write", phase: model.InflightRestoring},
		{name: "before a write: the live project it took down is still gone", phase: model.InflightRecreating},
		{name: "past a write, to its safety snapshot", phase: model.InflightRestoring, toSafety: true},
		{name: "a failed revert, to its safety snapshot", phase: model.InflightInconsistent, toSafety: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := demoResolution(record)
			stages := newMockStages(true)
			stages.target.prepared.summary = provider.PreparedSummary{
				Volumes:     []model.Volume{demoVolume()},
				Overwritten: []string{"demo_db"},
			}
			mock.InOrder(
				stages.expectInspect(nil),
				stages.expectPrepare(record.ID, demoPrepareOptions(record, true), nil),
				stages.expectLiveTeardown(nil),
				stages.target.expectBuild(errors.New("create boom")),
				stages.target.expectDiscard(nil),
			)
			inflight := &fakeInflight{}
			require.NoError(t, inflight.Put(context.Background(), newRecoveredMarker(record.ID, tt.phase, tt.toSafety)))
			svc := newStagedService(t, stages, savepoints, locker, inflight)
			recorder := &warnRecorder{}
			ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, fixedNow))

			// Act
			_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

			// Assert: the interrupted rollback's partial writes are not warned of.
			require.ErrorContains(t, err, "create boom")
			assert.Empty(t, recorder.inResult)
			marker, marked, getErr := inflight.Get(ctx)
			require.NoError(t, getErr)
			require.True(t, marked)
			assert.Equal(t, model.InflightRestoring, marker.Phase)
			assert.Equal(t, record.ID, marker.RecoveryID())
			savepoints.AssertExpectations(t)
			locker.AssertExpectations(t)
			stages.AssertExpectations(t)
		})
	}
}

// A failed recovery to the safety snapshot keeps naming it, and frees the gate: a snapshot that
// cannot come up must hold nothing hostage, and the hint must not deny that it exists.
func TestService_Rollback_FailedRecoveryKeepsTheSafetySnapshot(t *testing.T) {
	// Arrange
	record := demoRecord()
	standing := newRecoveredMarker(record.ID, model.InflightInconsistent, true)
	inflight := &fakeInflight{}
	require.NoError(t, inflight.Put(context.Background(), standing))
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, true), nil),
		stages.expectLiveTeardown(errors.New("down failed")),
	)
	svc := newStagedService(t, stages, savepoints, locker, inflight)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{ID: ptrID(record.ID), WaitTimeout: 60 * time.Second})

	// Assert
	require.ErrorContains(t, err, "down failed")
	assert.False(t, svc.isRecoveryRollback(standing, demoSafetySnapshot().ID), "before it, only the snapshot recovered")
	marker, found, getErr := inflight.Get(ctx)
	require.NoError(t, getErr)
	require.True(t, found)
	assert.Equal(t, record.ID, marker.Safety)
	assert.Equal(t, model.InflightRestoring, marker.Phase)
	assert.True(t, svc.isRecoveryRollback(marker, demoSafetySnapshot().ID), "another savepoint may recover now")
	hint := workflow.RecoveryHint(marker)
	assert.Contains(t, hint, "the recovery to the safety snapshot failed")
	assert.Contains(t, hint, record.ID.String())
	assert.Contains(t, hint, "or name another savepoint")
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// Once the start has passed the rollback has succeeded: an interrupt that lands then still reaps on
// a live context and clears the marker.
func TestService_Rollback_NoSafetySnapshotCleansUpPastAnInterrupt(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := demoResolution(record)
	stages := newMockStages(false)
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	live := mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPrepare(record.ID, demoPrepareOptions(record, false), nil),
		stages.expectLiveTeardown(nil),
		stages.target.expectBuild(nil),
		stages.target.expectRestoreData(nil),
		stages.target.expectStart(60*time.Second, nil).Run(func(mock.Arguments) { cancel() }),
		stages.target.restored.On("Reap", live).Once(),
	)
	inflight := &fakeInflight{}
	svc := newStagedService(t, stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Rollback(ctx, model.RollbackRequest{
		ID: ptrID(record.ID), WaitTimeout: 60 * time.Second, SkipSafetySnapshot: true,
	})

	// Assert
	require.NoError(t, err)
	_, marked, getErr := inflight.Get(context.Background())
	require.NoError(t, getErr)
	assert.False(t, marked)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}
