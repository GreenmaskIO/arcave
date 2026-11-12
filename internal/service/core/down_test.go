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
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// featSession is a session a down does not run in.
var featSession = model.SessionRef{ID: otherSession, Name: "feat"}

// deploymentOf is a build of demoRecord from session of arcaveProject.
func deploymentOf(arcaveProject model.ArcaveProjectName, session model.SessionRef, at time.Time) model.SavepointDeployment {
	return model.SavepointDeployment{
		DeployedAt:      at,
		ArcaveProject:   arcaveProject,
		Session:         session,
		Savepoint:       model.DeployedSavepoint{ID: demoRecord().ID, Name: demoRecord().Name, CreatedAt: demoRecord().CreatedAt},
		ServiceReplicas: map[string][]int{demoService: {1}},
	}
}

// baseline is the session a down test runs in.
var baseline = model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}

// resourcesOf is a compose project of db replicas carrying labels, one map per replica, with its
// volume and network.
func resourcesOf(labels ...map[string]string) model.ComposeProjectResources {
	replicas := make([]model.Container, 0, len(labels))
	names := make([]string, 0, len(labels))
	for i, replicaLabels := range labels {
		replicas = append(replicas, model.Container{
			ID: fmt.Sprintf("c%d", i+1), Name: demoContainerName(i + 1), Number: i + 1, Labels: replicaLabels,
		})
		names = append(names, demoContainerName(i+1))
	}
	return model.ComposeProjectResources{
		Containers: names,
		Replicas:   model.ComposeProjectContainers{Services: []model.ServiceContainers{{Name: demoService, Containers: replicas}}},
		Volumes:    []string{demoVolumeName},
		Networks:   []string{demoProject + "_default"},
	}
}

// downDoubles are what a DownService test sets its expectations on.
type downDoubles struct {
	remover   *mockVolumeRemover
	locker    *mockLocker
	inspector *mockInspector
	runner    *mockComposeRunner
	tags      *mockLayerTags
	inflight  *fakeInflight
}

// expectGone is the read back after a drop that left nothing.
func (d downDoubles) expectGone() *mock.Call {
	return d.inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
}

func (d downDoubles) assert(t *testing.T) {
	t.Helper()
	d.locker.AssertExpectations(t)
	d.inspector.AssertExpectations(t)
	d.runner.AssertExpectations(t)
	d.tags.AssertExpectations(t)
	d.remover.AssertExpectations(t)
}

// newDownService wires a DownService in baseline of demoArcaveProject, every double empty but
// the free lock.
func newDownService() (*DownService, downDoubles) {
	doubles := downDoubles{
		locker: &mockLocker{}, inspector: &mockInspector{}, runner: &mockComposeRunner{},
		tags: &mockLayerTags{}, inflight: &fakeInflight{}, remover: &mockVolumeRemover{},
	}
	expectLock(doubles.locker)
	return NewDownService(DownDeps{
		ProjectLocker:  doubles.locker,
		Inflight:       doubles.inflight,
		ArcaveProject:  demoArcaveProject,
		ComposeProject: demoProject,
		Resources:      doubles.inspector,
		ComposeRunner:  doubles.runner,
		Remover:        doubles.remover,
		LayerTags:      doubles.tags,
	}), doubles
}

func TestFindDeploymentOwner(t *testing.T) {
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	own := deploymentOf(demoArcaveProject, baseline, at)
	newer := deploymentOf(demoArcaveProject, baseline, at.Add(time.Minute))
	foreignSession := deploymentOf(demoArcaveProject, featSession, at)
	foreignProject := deploymentOf("blog", baseline, at)
	broken := own.Labels()
	broken[string(model.SystemLabelDeploymentDeployedAt)] = "yesterday"
	tests := []struct {
		name      string
		resources model.ComposeProjectResources
		want      *model.SavepointDeployment
		wantErr   error
	}{
		{name: "every replica is the session's", resources: resourcesOf(own.Labels(), newer.Labels()), want: &newer},
		{name: "another session's", resources: resourcesOf(foreignSession.Labels()), want: &foreignSession, wantErr: &model.DeployedElsewhereError{
			ArcaveProject: demoArcaveProject, Deployment: foreignSession,
		}},
		{name: "another arcave project's", resources: resourcesOf(foreignProject.Labels()), want: &foreignProject, wantErr: &model.DeployedElsewhereError{
			ArcaveProject: demoArcaveProject, Deployment: foreignProject,
		}},
		{
			name: "one replica without labels", resources: resourcesOf(own.Labels(), map[string]string{}), want: &own,
			wantErr: &model.NotDeployedByArcaveError{Replicas: []string{demoService + "-2"}},
		},
		{
			name: "unreadable labels", resources: resourcesOf(broken), wantErr: &model.NotDeployedByArcaveError{Replicas: []string{demoService + "-1"}},
		},
		{
			name: "no labels beats another session", resources: resourcesOf(foreignSession.Labels(), map[string]string{}), want: &foreignSession,
			wantErr: &model.NotDeployedByArcaveError{Replicas: []string{demoService + "-2"}},
		},
		{
			name:      "no replica, a lone one-off",
			resources: model.ComposeProjectResources{Containers: []string{"demo-db-run-1"}},
			wantErr:   &model.NotDeployedByArcaveError{Containers: []string{"demo-db-run-1"}},
		},
		{
			name:      "no replica, a volume and a network left",
			resources: model.ComposeProjectResources{Volumes: []string{demoVolumeName}, Networks: []string{"demo_default"}},
			wantErr:   &model.NotDeployedByArcaveError{Volumes: []string{demoVolumeName}, Networks: []string{"demo_default"}},
		},
		{name: "nothing at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := findDeploymentOwner(tt.resources, demoArcaveProject, baseline.ID)

			// Assert
			assert.Equal(t, tt.wantErr, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The session's own environment goes whole once the labels are checked, then its restored layer
// tags, of no other origin; a tag that will not come off is a notice, one already gone nothing.
func TestDownService_Down_TakesDownItsOwnDeployment(t *testing.T) {
	// Arrange
	deployment := deploymentOf(demoArcaveProject, baseline, time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC))
	resources := resourcesOf(deployment.Labels())
	svc, doubles := newDownService()
	read := doubles.inspector.On("ReadResources", mock.Anything).Return(resources, nil).Once()
	drop := doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
	gone := doubles.expectGone()
	origin := model.LayerOrigin{ArcaveProject: demoArcaveProject, ComposeProject: demoProject}
	tag := func(n string) model.ImageRef { return model.ImageRef("arcave/demo-db-1-postgres:sp-" + n) }
	list := doubles.tags.On("ListLayerImages", mock.Anything).Return([]model.TaggedImage{
		{Ref: tag("1"), Origin: origin},
		{Ref: tag("2"), Origin: model.LayerOrigin{ArcaveProject: demoArcaveProject, ComposeProject: "other"}},
		{Ref: tag("3"), Origin: model.LayerOrigin{ArcaveProject: "blog", ComposeProject: demoProject}},
		{Ref: tag("4")},
		{Ref: tag("5"), Origin: origin},
		{Ref: tag("6"), Origin: origin},
	}, nil).Once()
	untag1 := doubles.tags.On("RemoveImageRef", mock.Anything, tag("1")).Return(nil).Once()
	untag5 := doubles.tags.On("RemoveImageRef", mock.Anything, tag("5")).
		Return(fmt.Errorf("remove image reference: %w", model.ErrImageRefNotFound)).Once()
	untag6 := doubles.tags.On("RemoveImageRef", mock.Anything, tag("6")).Return(errors.New("image is used by container c0ffee")).Once()
	mock.InOrder(read, drop, gone, list, untag1, untag5, untag6)

	// Act
	res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, res.Notices, 1)
	assert.Contains(t, res.Notices[0].Message, string(tag("6")))
	assert.Equal(t, model.DownResult{
		ComposeProject: demoProject,
		Session:        baseline,
		Deployment:     &deployment,
		Discarded: model.DiscardedResources{
			Containers: []string{demoContainerName(1)}, Volumes: []string{demoVolumeName}, Networks: []string{demoProject + "_default"},
		},
		RemovedTags: []model.ImageRef{tag("1")},
		Notices:     res.Notices,
	}, res)
	doubles.assert(t)
}

// What is not the session's refuses the down after the read and before anything goes: the runner
// and the tags carry no expectation. --force takes it down all the same.
func TestDownService_Down_ChecksTheOwnerFirst(t *testing.T) {
	foreign := deploymentOf(demoArcaveProject, featSession, time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC))
	tests := []struct {
		name      string
		resources model.ComposeProjectResources
		wantErr   error
	}{
		{name: "another session's", resources: resourcesOf(foreign.Labels()), wantErr: model.ErrDeployedElsewhere},
		{name: "compose's own", resources: resourcesOf(map[string]string{}), wantErr: model.ErrNotDeployedByArcave},
		{name: "a volume left", resources: model.ComposeProjectResources{Volumes: []string{demoVolumeName}}, wantErr: model.ErrNotDeployedByArcave},
		{name: "a lone one-off", resources: model.ComposeProjectResources{Containers: []string{"demo-db-run-1"}}, wantErr: model.ErrNotDeployedByArcave},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc, doubles := newDownService()
			doubles.inspector.On("ReadResources", mock.Anything).Return(tt.resources, nil).Once()

			// Act
			_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			doubles.assert(t)
		})
		t.Run(tt.name+", forced", func(t *testing.T) {
			// Arrange
			svc, doubles := newDownService()
			read := doubles.inspector.On("ReadResources", mock.Anything).Return(tt.resources, nil).Once()
			calls := []*mock.Call{read}
			// A container that is no replica goes first, its anonymous volumes with it.
			for _, name := range tt.resources.NonReplicas() {
				calls = append(calls, doubles.remover.On("RemoveContainer", mock.Anything, name).Return(nil).Once())
			}
			drop := doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
			gone := doubles.expectGone()
			list := doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, nil).Once()
			mock.InOrder(append(calls, drop, gone, list)...)

			// Act
			res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

			// Assert
			require.NoError(t, err)
			assert.True(t, res.Forced)
			assert.ElementsMatch(t, tt.resources.Containers, res.Discarded.Containers)
			assert.ElementsMatch(t, tt.resources.Volumes, res.Discarded.Volumes)
			doubles.assert(t)
		})
	}
}

// With nothing on the daemon the down is a success that drops nothing; the tags are still taken.
func TestDownService_Down_NothingToTakeDown(t *testing.T) {
	// Arrange
	svc, doubles := newDownService()
	doubles.inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
	doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, nil).Once()

	// Act
	res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.DiscardedResources{Containers: []string{}, Volumes: []string{}, Networks: []string{}}, res.Discarded)
	assert.Equal(t, []model.ImageRef{}, res.RemovedTags)
	doubles.assert(t)
}

// Another compose project's marker refuses the down forced or not, nothing read; this compose
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
			svc, doubles := newDownService()
			doubles.inflight.marker = tt.marker

			// Act
			_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: tt.force})

			// Assert
			var interrupted *model.InterruptedRollbackError
			require.ErrorAs(t, err, &interrupted)
			assert.NotNil(t, doubles.inflight.marker, "the marker stands")
			doubles.assert(t)
		})
	}

	t.Run("this compose project's, forced", func(t *testing.T) {
		// Arrange
		standing := marker(demoProject)
		svc, doubles := newDownService()
		inflight := &mockInflight{}
		gate := inflight.On("Get", mock.Anything).Return(*standing, true, nil).Once()
		svc.inflightMarker = inflight
		read := doubles.inspector.On("ReadResources", mock.Anything).Return(resourcesOf(map[string]string{}), nil).Once()
		drop := doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
		gone := doubles.expectGone()
		clear := inflight.On("Clear", mock.Anything).Return(nil).Once()
		list := doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, nil).Once()
		mock.InOrder(gate, read, drop, gone, clear, list)

		// Act
		res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

		// Assert
		require.NoError(t, err)
		assert.Equal(t, standing, res.ClearedInflight)
		require.Len(t, res.Notices, 1)
		assert.True(t, res.Notices[0].Severe)
		assert.Contains(t, res.Notices[0].Message, standing.Safety.String())
		doubles.assert(t)
		inflight.AssertExpectations(t)
	})
}

// A failed drop is the down's error; nothing after it runs.
func TestDownService_Down_DropFailure(t *testing.T) {
	// Arrange
	svc, doubles := newDownService()
	doubles.inspector.On("ReadResources", mock.Anything).Return(resourcesOf(map[string]string{}), nil).Once()
	doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(errors.New("daemon gone")).Once()

	// Act
	_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

	// Assert
	require.ErrorContains(t, err, "daemon gone")
	doubles.assert(t)
}

// A down proves its drop: a volume something outside the compose project holds refuses it, the
// marker and the tags left as they were; a network left only says so.
func TestDownService_Down_ProvesTheDrop(t *testing.T) {
	busy := errors.New("remove volume demo_db: volume is in use - [f00d]")

	t.Run("a busy volume", func(t *testing.T) {
		// Arrange: the tags and the marker carry no expectation past the gate.
		svc, doubles := newDownService()
		remover := doubles.remover
		read := doubles.inspector.On("ReadResources", mock.Anything).Return(resourcesOf(map[string]string{}), nil).Once()
		drop := doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
		left := model.ComposeProjectResources{Volumes: []string{demoVolumeName}}
		reread := doubles.inspector.On("ReadResources", mock.Anything).Return(left, nil).Once()
		remove := remover.On("RemoveVolume", mock.Anything, demoVolumeName).Return(busy).Once()
		again := doubles.inspector.On("ReadResources", mock.Anything).Return(left, nil).Once()
		mock.InOrder(read, drop, reread, remove, again)

		// Act
		_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

		// Assert
		require.ErrorIs(t, err, model.ErrComposeProjectLeft)
		assert.ErrorContains(t, err, demoVolumeName)
		doubles.assert(t)
		remover.AssertExpectations(t)
	})

	t.Run("a one-off goes before compose's down", func(t *testing.T) {
		// Arrange: compose keeps a running one-off of a service with no replica, and removes a
		// finished one without its anonymous volumes; removed first, it goes with them.
		svc, doubles := newDownService()
		oneoff := model.ComposeProjectResources{Containers: []string{"demo-db-run-1"}}
		read := doubles.inspector.On("ReadResources", mock.Anything).Return(oneoff, nil).Once()
		remove := doubles.remover.On("RemoveContainer", mock.Anything, "demo-db-run-1").Return(nil).Once()
		drop := doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
		gone := doubles.expectGone()
		list := doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, nil).Once()
		mock.InOrder(read, remove, drop, gone, list)

		// Act
		res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []string{"demo-db-run-1"}, res.Discarded.Containers)
		doubles.assert(t)
	})

	t.Run("what compose left goes by name, the network a container held too", func(t *testing.T) {
		// Arrange: the container holds the network, so it goes first.
		svc, doubles := newDownService()
		read := doubles.inspector.On("ReadResources", mock.Anything).Return(resourcesOf(map[string]string{}), nil).Once()
		drop := doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
		left := model.ComposeProjectResources{Containers: []string{demoContainerName(1)}, Networks: []string{demoProject + "_default"}}
		reread := doubles.inspector.On("ReadResources", mock.Anything).Return(left, nil).Once()
		removeContainer := doubles.remover.On("RemoveContainer", mock.Anything, demoContainerName(1)).Return(nil).Once()
		removeNetwork := doubles.remover.On("RemoveNetwork", mock.Anything, demoProject+"_default").Return(nil).Once()
		gone := doubles.expectGone()
		list := doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, nil).Once()
		mock.InOrder(read, drop, reread, removeContainer, removeNetwork, gone, list)

		// Act
		res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

		// Assert
		require.NoError(t, err)
		assert.Empty(t, res.Notices, "nothing was left to explain")
		doubles.assert(t)
	})

	t.Run("a network something else uses", func(t *testing.T) {
		// Arrange
		svc, doubles := newDownService()
		inUse := errors.New("error while removing network: network demo_default has active endpoints")
		doubles.inspector.On("ReadResources", mock.Anything).Return(resourcesOf(map[string]string{}), nil).Once()
		doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
		left := model.ComposeProjectResources{Networks: []string{demoProject + "_default"}}
		doubles.inspector.On("ReadResources", mock.Anything).Return(left, nil).Twice()
		doubles.remover.On("RemoveNetwork", mock.Anything, demoProject+"_default").Return(inUse).Once()
		doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, nil).Once()

		// Act
		res, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

		// Assert: the reason is the daemon's, not a guess.
		require.NoError(t, err)
		require.Len(t, res.Notices, 1)
		assert.Contains(t, res.Notices[0].Message, demoProject+"_default")
		assert.Contains(t, res.Notices[0].Message, "active endpoints")
		doubles.assert(t)
	})
}

// An interrupt right after the drop still clears the marker and takes the tags, on a context of
// their own: the environment they recorded is gone.
func TestDownService_Down_InterruptAfterTheDrop(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	defer cancel()
	svc, doubles := newDownService()
	doubles.inflight.marker = &model.InflightMarker{
		Phase: model.InflightRestoring, Session: model.DefaultSession, ComposeProject: demoProject, Target: demoRecord().ID,
	}
	doubles.inspector.On("ReadResources", mock.Anything).Return(resourcesOf(map[string]string{}), nil).Once()
	doubles.runner.On("DownWithVolumes", mock.Anything, demoProject).Return(nil).Once()
	doubles.inspector.On("ReadResources", mock.Anything).
		Run(func(mock.Arguments) { cancel() }).Return(model.ComposeProjectResources{}, nil).Once()
	doubles.tags.On("ListLayerImages", mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })).
		Return(nil, nil).Once()

	// Act
	res, err := svc.Down(ctx, model.DownRequest{Force: true})

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, res.ClearedInflight)
	assert.Nil(t, doubles.inflight.marker, "the marker went with the environment")
	doubles.assert(t)
}

// A marker that will not clear is the down's error, the environment already gone.
func TestDownService_Down_ClearFailure(t *testing.T) {
	// Arrange
	svc, doubles := newDownService()
	inflight := &mockInflight{}
	inflight.On("Get", mock.Anything).Return(model.InflightMarker{ComposeProject: demoProject, Target: demoRecord().ID}, true, nil).Once()
	inflight.On("Clear", mock.Anything).Return(errors.New("read-only file system")).Once()
	svc.inflightMarker = inflight
	doubles.inspector.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()

	// Act
	_, err := svc.Down(zerolog.Nop().WithContext(context.Background()), model.DownRequest{Force: true})

	// Assert
	require.ErrorContains(t, err, "the environment is down")
	require.ErrorContains(t, err, "read-only file system")
	doubles.assert(t)
	inflight.AssertExpectations(t)
}
