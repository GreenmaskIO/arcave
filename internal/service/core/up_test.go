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
)

// demoPresent is a running compose project as the survey names it: db's replica, its volume and
// its network.
func demoPresent() model.DiscardedResources {
	return model.DiscardedResources{
		Containers: []string{demoProject + "-db-1"},
		Volumes:    []string{"demo_db"},
		Networks:   []string{demoProject + "_default"},
	}
}

// nothingThere is a survey that found nothing of the compose project.
func nothingThere() model.DiscardedResources {
	return model.DiscardedResources{Containers: []string{}, Volumes: []string{}, Networks: []string{}}
}

// latestResolution is what an up of the newest savepoint opens with: the savepoints, listed once
// under the locks, and a free lock.
func latestResolution(records ...model.Savepoint) (*mockSavepoints, *mockLocker) {
	savepoints := &mockSavepoints{}
	savepoints.On("List", mock.Anything).Return(records, nil).Once()
	locker := &mockLocker{}
	expectLock(locker)
	return savepoints, locker
}

// Over a daemon that holds nothing of the compose project, up builds the newest savepoint: no
// selector is latest, the drop takes nothing, the marker names no safety snapshot, and nothing is
// discarded.
func TestService_Up_BuildsOverNothing(t *testing.T) {
	// Arrange
	older := demoRecord()
	older.ID, older.Name, older.CreatedAt = "019e843e-1650-7000-8000-0000000000aa", "older", older.CreatedAt.Add(-time.Hour)
	record := demoRecord()
	savepoints, locker := latestResolution(record, older)
	stages := newMockStages(true)
	stages.target.prepared.summary = demoSummary()
	inflight := &mockInflight{}
	mock.InOrder(
		inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
		stages.expectSurvey(nothingThere(), false, nil),
		stages.expectSurveyedPrepare(record.ID, demoPrepareOptions(record, false), nil),
		inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRecreating)).Return(nil).Once(),
		stages.expectDrop(nil),
		stages.expectVacant(),
		stages.target.expectBuild(nil),
		inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoring)).Return(nil).Once(),
		stages.target.expectRestoreData(nil),
		inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRestoreDone)).Return(nil).Once(),
		stages.target.expectStart(time.Minute, nil),
		stages.target.expectReap(),
		inflight.On("Clear", mock.Anything).Return(nil).Once(),
	)
	svc := newStagedService(stages, savepoints, locker, inflight)

	// Act
	res, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{WaitTimeout: time.Minute})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, record.ID, res.RolledBackTo.ID, "the newest savepoint")
	assert.Equal(t, demoSummary().Volumes, res.Restored)
	assert.Nil(t, res.Discarded)
	inflight.AssertExpectations(t)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// What the daemon still holds refuses the up right after the survey: nothing is prepared, marked
// or dropped, so the surveyed workload carries no expectation past it.
func TestService_Up_RefusesAPresentComposeProject(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := latestResolution(record)
	stages := newMockStages(false)
	stages.expectSurvey(demoPresent(), true, nil)
	inflight := &fakeInflight{}
	svc := newStagedService(stages, savepoints, locker, inflight)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.Up(ctx, model.UpRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrComposeProjectPresent)
	var present *model.ComposeProjectPresentError
	require.ErrorAs(t, err, &present)
	assert.Equal(t, &model.ComposeProjectPresentError{
		Containers: demoPresent().Containers, Volumes: demoPresent().Volumes,
	}, present)
	assert.Nil(t, inflight.marker)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A forced up over a present compose project prepares the savepoint, marks, and only then drops
// what is there, volumes included; the result names what went.
func TestService_Up_ForceDropsWhatIsThere(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := latestResolution(record)
	stages := newMockStages(false)
	inflight := &mockInflight{}
	mock.InOrder(
		inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once(),
		stages.expectSurvey(demoPresent(), true, nil),
		stages.expectSurveyedPrepare(record.ID, demoPrepareOptions(record, false), nil),
		inflight.On("Put", mock.Anything, isInflightMarker(record.ID, model.InflightRecreating)).Return(nil).Once(),
		stages.expectDrop(nil),
		stages.expectVacant(),
		stages.target.expectBuild(nil),
		stages.target.expectRestoreData(nil),
		stages.target.expectStart(0, nil),
		stages.target.expectReap(),
		inflight.On("Clear", mock.Anything).Return(nil).Once(),
	)
	inflight.On("Put", mock.Anything, mock.Anything).Return(nil)
	svc := newStagedService(stages, savepoints, locker, inflight)

	// Act
	res, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Force: true})

	// Assert
	require.NoError(t, err)
	want := demoPresent()
	assert.Equal(t, &want, res.Discarded)
	inflight.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A forced up that fails past the marker removes what it built; nothing was restored, so the
// marker, still recreating, clears. A drop that fails leaves nothing to discard.
func TestService_Up_ForceFailure(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name     string
		dropErr  error
		buildErr error
	}{
		{name: "the drop fails", dropErr: &model.ComposeProjectLeftError{Volumes: []string{"demo_db"}, Cause: errBoom}},
		{name: "the build fails", buildErr: errBoom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			savepoints, locker := latestResolution(record)
			stages := newMockStages(false)
			stages.expectSurvey(demoPresent(), true, nil)
			stages.expectSurveyedPrepare(record.ID, demoPrepareOptions(record, false), nil)
			stages.expectDrop(tt.dropErr)
			if tt.dropErr == nil {
				stages.expectVacant()
				stages.target.expectBuild(tt.buildErr)
				stages.target.expectDiscard(nil)
			}
			inflight := &fakeInflight{}
			svc := newStagedService(stages, savepoints, locker, inflight)

			// Act
			_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Force: true})

			// Assert
			require.ErrorContains(t, err, "up failed")
			if tt.dropErr != nil {
				require.ErrorIs(t, err, model.ErrComposeProjectLeft)
			} else {
				require.ErrorIs(t, err, errBoom)
			}
			assert.Nil(t, inflight.marker, "nothing was restored yet, so the marker clears")
			stages.AssertExpectations(t)
		})
	}
}

// An interrupt that lands while the savepoint is prepared stops a forced up before the marker and
// the drop: what the daemon holds is untouched.
func TestService_Up_InterruptBeforeTheDrop(t *testing.T) {
	// Arrange
	record := demoRecord()
	savepoints, locker := latestResolution(record)
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	stages := newMockStages(false)
	stages.expectSurvey(demoPresent(), true, nil)
	stages.expectSurveyedPrepare(record.ID, demoPrepareOptions(record, false), nil).Run(func(mock.Arguments) { cancel() })
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{}, false, nil).Once()
	svc := newStagedService(stages, savepoints, locker, inflight)

	// Act
	_, err := svc.Up(ctx, model.UpRequest{Force: true})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	inflight.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// Under an inflight marker up refuses, forced or not and whatever it names: recovering is a
// rollback's. Nothing past the gate is read.
func TestService_Up_RefusesUnderAMarker(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "unforced", true: "forced"}[force], func(t *testing.T) {
			// Arrange: the marker names the very savepoint up would build.
			record := demoRecord()
			savepoints, locker := latestResolution(record)
			stages := newMockStages(false)
			inflight := &fakeInflight{marker: &model.InflightMarker{
				Phase: model.InflightRecreating, Session: model.DefaultSession, ComposeProject: demoProject,
				Target: record.ID,
			}}
			svc := newStagedService(stages, savepoints, locker, inflight)

			// Act
			_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Force: force})

			// Assert
			var interrupted *model.InterruptedRollbackError
			require.ErrorAs(t, err, &interrupted)
			stages.AssertExpectations(t)
		})
	}
}

// A session with no savepoint to bring up says so by the sentinel.
func TestService_Up_NoSavepoints(t *testing.T) {
	// Arrange
	savepoints, locker := latestResolution()
	stages := newMockStages(true)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})

	// Act
	_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrNoSavepoints)
	savepoints.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// A savepoint named and not there is refused before the wait on the lock, as a rollback's: the
// locker carries no expectation.
func TestService_Up_RefusesAMissingSavepointBeforeTheLock(t *testing.T) {
	// Arrange
	savepoints := &mockSavepoints{}
	savepoints.On("List", mock.Anything).Return([]model.Savepoint{demoRecord()}, nil).Once()
	locker := &mockLocker{}
	stages := newMockStages(true)
	svc := newStagedService(stages, savepoints, locker, &fakeInflight{})

	// Act
	_, err := svc.Up(zerolog.Nop().WithContext(context.Background()), model.UpRequest{Name: ptrString("clean")})

	// Assert
	require.ErrorIs(t, err, errSavepointNotFound)
	savepoints.AssertExpectations(t)
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}
