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

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// featSession is a session a down does not run in.
var featSession = model.SessionRef{
	ID:   model.SessionID(uuid.MustParse("7d1e3b2a-4c5f-4a6e-8b9d-0e1f2a3b4c5d")),
	Name: "feat",
}

// demoOwnDeployment is a build of demoRecord in the default session of demoArcaveProject.
func demoOwnDeployment() model.SavepointDeployment {
	return model.SavepointDeployment{
		DeployedAt:      time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC),
		ArcaveProject:   demoArcaveProject,
		Session:         defaultSessionRef,
		Savepoint:       model.DeployedSavepoint{ID: demoRecord().ID, Name: demoRecord().Name, CreatedAt: demoRecord().CreatedAt},
		ServiceReplicas: map[string][]int{"db": {1}},
	}
}

// newDownService wires a DownService over stages in the default session of demoArcaveProject,
// with a free lock.
func newDownService(stages *mockStages, inflight inflightMarkers) (*DownService, *mockLocker) {
	locker := &mockLocker{}
	expectLock(locker)
	return NewDownService(DownDeps{
		ProjectLocker:  locker,
		Inflight:       inflight,
		ArcaveProject:  demoArcaveProject,
		ComposeProject: demoProject,
		Workload:       &stages.workload,
	}), locker
}

// The session's own environment goes whole once its owner is judged, then its restored layer
// tags; what the drop and the untag left are the result's notices, the drop's first.
func TestDownService_Down_TakesDownItsOwnDeployment(t *testing.T) {
	// Arrange
	deployment := demoOwnDeployment()
	stages := newMockStages(false)
	left := model.Notice{Message: "network demo_default left behind: in use"}
	stuck := model.Notice{Message: "restored layer tag x left behind: in use"}
	stages.dropped.notices = []model.Notice{left}
	removed := []model.ImageRef{"arcave/demo-db-1-postgres:sp-1"}
	mock.InOrder(
		stages.expectSurvey(demoPresent(), true, nil),
		stages.expectOwner(&deployment, nil),
		stages.expectDrop(nil),
		stages.expectRemoveRestoredLayers(removed, []model.Notice{stuck}),
	)
	svc, locker := newDownService(stages, &fakeInflight{})
	recorder := &warnRecorder{}
	ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, fixedNow))

	// Act
	res, err := svc.Down(ctx, model.DownRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.DownResult{
		ComposeProject: demoProject,
		Session:        defaultSessionRef,
		Deployment:     &deployment,
		Discarded:      demoPresent(),
		RemovedTags:    removed,
		Notices:        []model.Notice{left, stuck},
	}, res)
	assert.Equal(t, []string{left.Message}, recorder.inResult, "the network the drop left is a warning of the result")
	locker.AssertExpectations(t)
	stages.AssertExpectations(t)
}

// What is not the session's refuses the down after the survey and before anything goes. --force
// takes it down all the same, naming the deployment it read.
func TestDownService_Down_ChecksTheOwnerFirst(t *testing.T) {
	foreign := demoOwnDeployment()
	foreign.Session = featSession
	tests := []struct {
		name       string
		deployment *model.SavepointDeployment
		err        error
	}{
		{name: "another session's", deployment: &foreign, err: &model.DeployedElsewhereError{ArcaveProject: demoArcaveProject, Deployment: foreign}},
		{name: "compose's own", err: &model.NotDeployedByArcaveError{Replicas: []string{"db-1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			stages := newMockStages(false)
			stages.expectSurvey(demoPresent(), true, nil)
			stages.expectOwner(tt.deployment, tt.err)
			svc, _ := newDownService(stages, &fakeInflight{})

			// Act
			_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{})

			// Assert
			require.ErrorIs(t, err, tt.err)
			stages.AssertExpectations(t)
		})
		t.Run(tt.name+", forced", func(t *testing.T) {
			// Arrange
			stages := newMockStages(false)
			mock.InOrder(
				stages.expectSurvey(demoPresent(), true, nil),
				stages.expectOwner(tt.deployment, tt.err),
				stages.expectDrop(nil),
				stages.expectRemoveRestoredLayers([]model.ImageRef{}, nil),
			)
			svc, _ := newDownService(stages, &fakeInflight{})

			// Act
			res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

			// Assert
			require.NoError(t, err)
			assert.True(t, res.Forced)
			assert.Equal(t, tt.deployment, res.Deployment)
			assert.Equal(t, demoPresent(), res.Discarded)
			stages.AssertExpectations(t)
		})
	}
}

// Another compose project's marker refuses the down forced or not, nothing surveyed; this compose
// project's, from any session, refuses an unforced one and is cleared by a forced one, after the drop.
func TestDownService_Down_UnderAMarker(t *testing.T) {
	marker := func(composeProject model.DockerComposeProjectName) *model.InflightMarker {
		return &model.InflightMarker{
			Phase: model.InflightInconsistent, Session: featSession.ID, ComposeProject: composeProject,
			Target: demoRecord().ID, Safety: demoSafetySnapshot().ID,
		}
	}
	tests := []struct {
		name   string
		marker *model.InflightMarker
		force  bool
	}{
		{name: "another compose project's", marker: marker("other")},
		{name: "another compose project's, forced", marker: marker("other"), force: true},
		{name: "this compose project's", marker: marker(demoProject)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			stages := newMockStages(false)
			inflight := &fakeInflight{marker: tt.marker}
			svc, _ := newDownService(stages, inflight)

			// Act
			_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: tt.force})

			// Assert
			var interrupted *model.InterruptedRollbackError
			require.ErrorAs(t, err, &interrupted)
			assert.NotNil(t, inflight.marker, "the marker stands")
			stages.AssertExpectations(t)
		})
	}

	t.Run("this compose project's, forced", func(t *testing.T) {
		// Arrange
		standing := marker(demoProject)
		stages := newMockStages(false)
		inflight := &mockInflight{}
		mock.InOrder(
			inflight.On("Get", mock.Anything).Return(*standing, true, nil).Once(),
			stages.expectSurvey(demoPresent(), true, nil),
			stages.expectOwner(nil, &model.NotDeployedByArcaveError{Replicas: []string{"db-1"}}),
			stages.expectDrop(nil),
			inflight.On("Clear", mock.Anything).Return(nil).Once(),
			stages.expectRemoveRestoredLayers([]model.ImageRef{}, nil),
		)
		svc, _ := newDownService(stages, inflight)

		// Act
		res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

		// Assert
		require.NoError(t, err)
		assert.Equal(t, standing, res.ClearedInflight)
		require.Len(t, res.Notices, 1)
		assert.True(t, res.Notices[0].Severe)
		assert.Contains(t, res.Notices[0].Message, standing.Safety.String())
		stages.AssertExpectations(t)
		inflight.AssertExpectations(t)
	})
}

// A failed drop is the down's error; the marker and the tags are left as they were.
func TestDownService_Down_DropFailure(t *testing.T) {
	// Arrange
	stages := newMockStages(false)
	stages.expectSurvey(demoPresent(), true, nil)
	stages.expectOwner(nil, nil)
	left := &model.ComposeProjectLeftError{Volumes: []string{"demo_db"}, Cause: errors.New("volume is in use")}
	stages.expectDrop(left)
	standing := &model.InflightMarker{Phase: model.InflightRestoring, ComposeProject: demoProject, Target: demoRecord().ID}
	inflight := &fakeInflight{marker: standing}
	svc, _ := newDownService(stages, inflight)

	// Act
	_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

	// Assert
	require.ErrorIs(t, err, model.ErrComposeProjectLeft)
	assert.Equal(t, standing, inflight.marker, "the marker stands")
	stages.AssertExpectations(t)
}

// An interrupt right after the drop still clears the marker and takes the tags, on a context of
// their own: the environment they recorded is gone.
func TestDownService_Down_InterruptAfterTheDrop(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	stages := newMockStages(false)
	stages.expectSurvey(demoPresent(), true, nil)
	stages.expectOwner(nil, nil)
	stages.expectDrop(nil).Run(func(mock.Arguments) { cancel() })
	stages.dropped.On("RemoveRestoredLayers", mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })).
		Return([]model.ImageRef{}, []model.Notice(nil)).Once()
	inflight := &fakeInflight{marker: &model.InflightMarker{
		Phase: model.InflightRestoring, Session: model.DefaultSession, ComposeProject: demoProject, Target: demoRecord().ID,
	}}
	svc, _ := newDownService(stages, inflight)

	// Act
	res, err := svc.Down(ctx, model.DownRequest{Force: true})

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, res.ClearedInflight)
	assert.Nil(t, inflight.marker, "the marker went with the environment")
	stages.AssertExpectations(t)
}

// A marker that will not clear is the down's error, the environment already gone.
func TestDownService_Down_ClearFailure(t *testing.T) {
	// Arrange
	stages := newMockStages(false)
	stages.expectSurvey(nothingThere(), false, nil)
	stages.expectOwner(nil, nil)
	stages.expectDrop(nil)
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{ComposeProject: demoProject, Target: demoRecord().ID}, true, nil).Once()
	inflight.On("Clear", mock.Anything).Return(errors.New("read-only file system")).Once()
	svc, _ := newDownService(stages, inflight)

	// Act
	_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

	// Assert
	require.ErrorContains(t, err, "the environment is down")
	require.ErrorContains(t, err, "read-only file system")
	stages.AssertExpectations(t)
	inflight.AssertExpectations(t)
}
