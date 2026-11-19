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

package workload

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// demoSession is the session a drop test runs in, demoOtherSession another.
var (
	demoSession      = model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}
	demoOtherSession = model.SessionRef{ID: model.SessionID(uuid.MustParse("7d1e3b2a-4c5f-4a6e-8b9d-0e1f2a3b4c5d")), Name: "feat"}
)

// demoNetwork is the compose project's default network.
const demoNetwork = composetest.DemoProject + "_default"

// deploymentOf is a build of demoRecord from session of arcaveProject.
func deploymentOf(arcaveProject model.ArcaveProjectName, session model.SessionRef, at time.Time) composemodel.Deployment {
	return composemodel.Deployment{
		SavepointDeployment: model.SavepointDeployment{
			DeployedAt:    at,
			ArcaveProject: arcaveProject,
			Session:       session,
			Savepoint:     model.DeployedSavepoint{ID: composetest.DemoRecord().Header.ID, Name: composetest.DemoRecord().Header.Name, CreatedAt: composetest.DemoRecord().Header.CreatedAt},
		},
		ServiceReplicas: map[string][]int{composetest.DemoService: {1}},
	}
}

// resourcesOf is a compose project of db replicas carrying labels, one map per replica, with its
// volume and network.
func resourcesOf(labels ...map[string]string) composemodel.ComposeProjectResources {
	replicas := make([]composemodel.Container, 0, len(labels))
	names := make([]string, 0, len(labels))
	for i, replicaLabels := range labels {
		replicas = append(replicas, composemodel.Container{
			ID: fmt.Sprintf("c%d", i+1), Name: composetest.DemoContainerName(i + 1), Number: i + 1, Labels: replicaLabels,
		})
		names = append(names, composetest.DemoContainerName(i+1))
	}
	return composemodel.ComposeProjectResources{
		Containers: names,
		Replicas:   []composemodel.ServiceReplicas{{Name: composetest.DemoService, Containers: replicas}},
		Volumes:    []string{composetest.DemoVolumeName},
		Networks:   []string{demoNetwork},
	}
}

// dropDoubles are what a drop test sets its expectations on.
type dropDoubles struct {
	reader  *composetest.MockInspector
	downer  *composetest.MockComposeRunner
	volumes *composetest.MockVolumeRemover
	remover *composetest.MockProjectRemover
	tags    *composetest.MockLayerTags
}

func (d dropDoubles) assert(t *testing.T) {
	t.Helper()
	d.reader.AssertExpectations(t)
	d.downer.AssertExpectations(t)
	d.volumes.AssertExpectations(t)
	d.remover.AssertExpectations(t)
	d.tags.AssertExpectations(t)
}

// expectGone is the read back after a drop that left nothing.
func (d dropDoubles) expectGone() *mock.Call {
	return d.reader.On("ReadResources", mock.Anything).Return(composemodel.ComposeProjectResources{}, nil).Once()
}

// expectDown is compose's down of composetest.DemoProject by its name, failing with err.
func (d dropDoubles) expectDown(err error) *mock.Call {
	return d.downer.On("DownWithVolumes", mock.Anything, composetest.DemoProject).Return(err).Once()
}

// newDropWorkload wires the workload of composetest.DemoProject in composetest.DemoArcaveProject
// with only what a drop needs, every double empty.
func newDropWorkload(deps Deps) (*composeWorkload, dropDoubles) {
	doubles := dropDoubles{
		reader:  &composetest.MockInspector{},
		downer:  &composetest.MockComposeRunner{},
		volumes: &composetest.MockVolumeRemover{},
		remover: &composetest.MockProjectRemover{},
		tags:    &composetest.MockLayerTags{},
	}
	deps.ComposeReader = doubles.reader
	deps.ComposeRunner = doubles.downer
	deps.VolumeRemover = doubles.volumes
	deps.ProjectRemover = doubles.remover
	deps.LayerTags = doubles.tags
	deps.LayerOrigin = composemodel.LayerOrigin{
		ArcaveProject:  composetest.DemoArcaveProject,
		ComposeProject: composetest.DemoProject,
	}
	return newComposeWorkload(deps), doubles
}

// survey surveys svc over a reader answering resources.
func survey(t *testing.T, svc *composeWorkload, doubles dropDoubles, resources composemodel.ComposeProjectResources) provider.SurveyedWorkload {
	t.Helper()
	doubles.reader.On("ReadResources", mock.Anything).Return(resources, nil).Once()
	surveyed, err := svc.Survey(zerolog.Nop().WithContext(context.Background()))
	require.NoError(t, err)
	return surveyed
}

func TestSurveyed_Owner(t *testing.T) {
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	own := deploymentOf(composetest.DemoArcaveProject, demoSession, at)
	newer := deploymentOf(composetest.DemoArcaveProject, demoSession, at.Add(time.Minute))
	foreignSession := deploymentOf(composetest.DemoArcaveProject, demoOtherSession, at)
	foreignProject := deploymentOf("blog", demoSession, at)
	broken := composemodel.NewDeploymentLabels(own)
	broken[string(composemodel.SystemLabelDeploymentDeployedAt)] = "yesterday"
	tests := []struct {
		name      string
		resources composemodel.ComposeProjectResources
		want      *model.SavepointDeployment
		wantErr   error
	}{
		{name: "every replica is the session's", resources: resourcesOf(composemodel.NewDeploymentLabels(own), composemodel.NewDeploymentLabels(newer)), want: &newer.SavepointDeployment},
		{name: "another session's", resources: resourcesOf(composemodel.NewDeploymentLabels(foreignSession)), want: &foreignSession.SavepointDeployment, wantErr: &model.DeployedElsewhereError{
			ArcaveProject: composetest.DemoArcaveProject, Deployment: foreignSession.SavepointDeployment,
		}},
		{name: "another arcave project's", resources: resourcesOf(composemodel.NewDeploymentLabels(foreignProject)), want: &foreignProject.SavepointDeployment, wantErr: &model.DeployedElsewhereError{
			ArcaveProject: composetest.DemoArcaveProject, Deployment: foreignProject.SavepointDeployment,
		}},
		{
			name: "one replica without labels", resources: resourcesOf(composemodel.NewDeploymentLabels(own), map[string]string{}), want: &own.SavepointDeployment,
			wantErr: &composemodel.NotDeployedByArcaveError{Replicas: []string{composetest.DemoService + "-2"}},
		},
		{
			name: "unreadable labels", resources: resourcesOf(broken), wantErr: &composemodel.NotDeployedByArcaveError{Replicas: []string{composetest.DemoService + "-1"}},
		},
		{
			name: "no labels beats another session", resources: resourcesOf(composemodel.NewDeploymentLabels(foreignSession), map[string]string{}), want: &foreignSession.SavepointDeployment,
			wantErr: &composemodel.NotDeployedByArcaveError{Replicas: []string{composetest.DemoService + "-2"}},
		},
		{
			name:      "no replica, a lone one-off",
			resources: composemodel.ComposeProjectResources{Containers: []string{"demo-db-run-1"}},
			wantErr:   &composemodel.NotDeployedByArcaveError{Containers: []string{"demo-db-run-1"}},
		},
		{
			name:      "no replica, a volume and a network left",
			resources: composemodel.ComposeProjectResources{Volumes: []string{composetest.DemoVolumeName}, Networks: []string{demoNetwork}},
			wantErr:   &composemodel.NotDeployedByArcaveError{Volumes: []string{composetest.DemoVolumeName}, Networks: []string{demoNetwork}},
		},
		{name: "nothing at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc, doubles := newDropWorkload(Deps{})
			surveyed := survey(t, svc, doubles, tt.resources)

			// Act
			got, err := surveyed.Owner(composetest.DemoArcaveProject, demoSession.ID)

			// Assert
			assert.Equal(t, tt.wantErr, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A container or a volume is what a build would reuse; a network alone is not.
func TestSurveyed_Present(t *testing.T) {
	tests := []struct {
		name      string
		resources composemodel.ComposeProjectResources
		want      bool
	}{
		{name: "a running compose project", resources: resourcesOf(map[string]string{}), want: true},
		{name: "a volume compose's down kept", resources: composemodel.ComposeProjectResources{Volumes: []string{composetest.DemoVolumeName}}, want: true},
		{name: "a network alone", resources: composemodel.ComposeProjectResources{Networks: []string{demoNetwork}}},
		{name: "nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc, doubles := newDropWorkload(Deps{})

			// Act
			surveyed := survey(t, svc, doubles, tt.resources)

			// Assert
			assert.Equal(t, tt.want, surveyed.Present())
			assert.Equal(t, tt.resources.Discarded(), surveyed.Resources())
		})
	}
}

// A drop proves itself: compose's down by name, then by docker name whatever it left, and a read
// back; a container or a volume still there refuses it, a network left only says so.
func TestSurveyed_Drop(t *testing.T) {
	busy := errors.New("remove volume demo_db: volume is in use - [f00d]")

	t.Run("nothing to drop removes nothing", func(t *testing.T) {
		// Arrange: no double past the survey carries an expectation.
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, composemodel.ComposeProjectResources{})

		// Act
		dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.NoError(t, err)
		assert.Empty(t, dropped.Notices())
		doubles.assert(t)
	})

	t.Run("the whole compose project goes", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		mock.InOrder(doubles.expectDown(nil), doubles.expectGone())

		// Act
		dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.NoError(t, err)
		assert.Empty(t, dropped.Notices())
		doubles.assert(t)
	})

	t.Run("a one-off goes before compose's down", func(t *testing.T) {
		// Arrange: compose keeps a running one-off of a service with no replica, and removes a
		// finished one without its anonymous volumes; removed first, it goes with them.
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, composemodel.ComposeProjectResources{Containers: []string{"demo-db-run-1"}})
		mock.InOrder(
			doubles.remover.On("RemoveContainer", mock.Anything, "demo-db-run-1").Return(nil).Once(),
			doubles.expectDown(nil),
			doubles.expectGone(),
		)

		// Act
		_, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.NoError(t, err)
		doubles.assert(t)
	})

	t.Run("what compose left goes by name, the network a container held too", func(t *testing.T) {
		// Arrange: the container holds the network, so it goes first.
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		left := composemodel.ComposeProjectResources{
			Containers: []string{composetest.DemoContainerName(1)}, Volumes: []string{"demo_extra"}, Networks: []string{demoNetwork},
		}
		mock.InOrder(
			doubles.expectDown(nil),
			doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Once(),
			doubles.remover.On("RemoveContainer", mock.Anything, composetest.DemoContainerName(1)).Return(nil).Once(),
			doubles.volumes.On("RemoveVolume", mock.Anything, "demo_extra").Return(nil).Once(),
			doubles.remover.On("RemoveNetwork", mock.Anything, demoNetwork).Return(nil).Once(),
			doubles.expectGone(),
		)

		// Act
		dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.NoError(t, err)
		assert.Empty(t, dropped.Notices(), "nothing was left to explain")
		doubles.assert(t)
	})

	t.Run("a busy volume refuses the drop", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		left := composemodel.ComposeProjectResources{Volumes: []string{composetest.DemoVolumeName}}
		mock.InOrder(
			doubles.expectDown(nil),
			doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Once(),
			doubles.volumes.On("RemoveVolume", mock.Anything, composetest.DemoVolumeName).Return(busy).Once(),
			doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Once(),
		)

		// Act
		_, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.ErrorIs(t, err, model.ErrComposeProjectLeft)
		var leftErr *composemodel.ComposeProjectLeftError
		require.ErrorAs(t, err, &leftErr)
		assert.Equal(t, []string{composetest.DemoVolumeName}, leftErr.Volumes)
		assert.ErrorIs(t, err, busy, "the daemon's refusal says what holds it")
		doubles.assert(t)
	})

	t.Run("a network something else uses stays, with the daemon's reason", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		inUse := errors.New("error while removing network: network demo_default has active endpoints")
		left := composemodel.ComposeProjectResources{Networks: []string{demoNetwork}}
		doubles.expectDown(nil)
		doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Twice()
		doubles.remover.On("RemoveNetwork", mock.Anything, demoNetwork).Return(inUse).Once()

		// Act
		dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.NoError(t, err)
		notices := dropped.Notices()
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0].Message, demoNetwork)
		assert.Contains(t, notices[0].Message, "active endpoints")
		doubles.assert(t)
	})

	t.Run("compose's down fails", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropWorkload(Deps{})
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		doubles.expectDown(errors.New("daemon gone"))

		// Act
		_, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.ErrorContains(t, err, "daemon gone")
		doubles.assert(t)
	})
}

// A daemon the build cannot run on refuses the prepare, before anything is loaded.
func TestSurveyed_PrepareSavepoint_RefusesAnOldDaemon(t *testing.T) {
	// Arrange: the repository carries no expectation, so a load fails the test.
	svc, doubles := newDropWorkload(Deps{DaemonVersion: "24.0.7", MinDaemonMajor: 25})
	surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))

	// Act
	_, err := surveyed.PrepareSavepoint(zerolog.Nop().WithContext(context.Background()), composetest.DemoRecord().Header.ID,
		provider.PrepareOptions{Deployment: composetest.DemoDeployment(composetest.DemoRecord())})

	// Assert
	require.ErrorIs(t, err, composemodel.ErrUnsupportedDaemon)
}
