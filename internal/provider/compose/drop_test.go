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

package compose

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
)

// demoSession is the session a drop test runs in, demoOtherSession another.
var (
	demoSession      = model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}
	demoOtherSession = model.SessionRef{ID: model.SessionID(uuid.MustParse("7d1e3b2a-4c5f-4a6e-8b9d-0e1f2a3b4c5d")), Name: "feat"}
)

// demoNetwork is the compose project's default network.
const demoNetwork = demoProject + "_default"

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
		Networks:   []string{demoNetwork},
	}
}

// dropDoubles are what a drop test sets its expectations on.
type dropDoubles struct {
	reader  *mockInspector
	downer  *mockVolumesDowner
	remover *mockProjectRemover
	tags    *mockLayerTags
}

func (d dropDoubles) assert(t *testing.T) {
	t.Helper()
	d.reader.AssertExpectations(t)
	d.downer.AssertExpectations(t)
	d.remover.AssertExpectations(t)
	d.tags.AssertExpectations(t)
}

// expectGone is the read back after a drop that left nothing.
func (d dropDoubles) expectGone() *mock.Call {
	return d.reader.On("ReadResources", mock.Anything).Return(model.ComposeProjectResources{}, nil).Once()
}

// expectDown is compose's down of demoProject by its name, failing with err.
func (d dropDoubles) expectDown(err error) *mock.Call {
	return d.downer.On("DownWithVolumes", mock.Anything, demoProject).Return(err).Once()
}

// newDropProvider wires a Provider of demoProject in demoArcaveProject with only what a drop
// needs, every double empty.
func newDropProvider() (*Provider, dropDoubles) {
	doubles := dropDoubles{
		reader: &mockInspector{}, downer: &mockVolumesDowner{}, remover: &mockProjectRemover{}, tags: &mockLayerTags{},
	}
	return New(Deps{
		ArcaveProject:  demoArcaveProject,
		ComposeProject: demoProject,
		ResourceReader: doubles.reader,
		VolumesDowner:  doubles.downer,
		ProjectRemover: doubles.remover,
		LayerTags:      doubles.tags,
	}), doubles
}

// survey surveys svc over a reader answering resources.
func survey(t *testing.T, svc *Provider, doubles dropDoubles, resources model.ComposeProjectResources) provider.SurveyedWorkload {
	t.Helper()
	doubles.reader.On("ReadResources", mock.Anything).Return(resources, nil).Once()
	surveyed, err := svc.Survey(zerolog.Nop().WithContext(context.Background()))
	require.NoError(t, err)
	return surveyed
}

func TestSurveyed_Owner(t *testing.T) {
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	own := deploymentOf(demoArcaveProject, demoSession, at)
	newer := deploymentOf(demoArcaveProject, demoSession, at.Add(time.Minute))
	foreignSession := deploymentOf(demoArcaveProject, demoOtherSession, at)
	foreignProject := deploymentOf("blog", demoSession, at)
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
			resources: model.ComposeProjectResources{Volumes: []string{demoVolumeName}, Networks: []string{demoNetwork}},
			wantErr:   &model.NotDeployedByArcaveError{Volumes: []string{demoVolumeName}, Networks: []string{demoNetwork}},
		},
		{name: "nothing at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc, doubles := newDropProvider()
			surveyed := survey(t, svc, doubles, tt.resources)

			// Act
			got, err := surveyed.Owner(demoArcaveProject, demoSession.ID)

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
		resources model.ComposeProjectResources
		want      bool
	}{
		{name: "a running compose project", resources: resourcesOf(map[string]string{}), want: true},
		{name: "a volume compose's down kept", resources: model.ComposeProjectResources{Volumes: []string{demoVolumeName}}, want: true},
		{name: "a network alone", resources: model.ComposeProjectResources{Networks: []string{demoNetwork}}},
		{name: "nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc, doubles := newDropProvider()

			// Act
			surveyed := survey(t, svc, doubles, tt.resources)

			// Assert
			assert.Equal(t, tt.want, surveyed.Present())
			assert.Equal(t, model.NewDiscardedResources(tt.resources), surveyed.Resources())
		})
	}
}

// A drop proves itself: compose's down by name, then by docker name whatever it left, and a read
// back; a container or a volume still there refuses it, a network left only says so.
func TestSurveyed_Drop(t *testing.T) {
	busy := errors.New("remove volume demo_db: volume is in use - [f00d]")

	t.Run("nothing to drop removes nothing", func(t *testing.T) {
		// Arrange: no double past the survey carries an expectation.
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, model.ComposeProjectResources{})

		// Act
		dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.NoError(t, err)
		assert.Empty(t, dropped.Notices())
		doubles.assert(t)
	})

	t.Run("the whole compose project goes", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropProvider()
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
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, model.ComposeProjectResources{Containers: []string{"demo-db-run-1"}})
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
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		left := model.ComposeProjectResources{
			Containers: []string{demoContainerName(1)}, Volumes: []string{"demo_extra"}, Networks: []string{demoNetwork},
		}
		mock.InOrder(
			doubles.expectDown(nil),
			doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Once(),
			doubles.remover.On("RemoveContainer", mock.Anything, demoContainerName(1)).Return(nil).Once(),
			doubles.remover.On("RemoveVolume", mock.Anything, "demo_extra").Return(nil).Once(),
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
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		left := model.ComposeProjectResources{Volumes: []string{demoVolumeName}}
		mock.InOrder(
			doubles.expectDown(nil),
			doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Once(),
			doubles.remover.On("RemoveVolume", mock.Anything, demoVolumeName).Return(busy).Once(),
			doubles.reader.On("ReadResources", mock.Anything).Return(left, nil).Once(),
		)

		// Act
		_, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.ErrorIs(t, err, model.ErrComposeProjectLeft)
		var leftErr *model.ComposeProjectLeftError
		require.ErrorAs(t, err, &leftErr)
		assert.Equal(t, []string{demoVolumeName}, leftErr.Volumes)
		assert.ErrorIs(t, err, busy, "the daemon's refusal says what holds it")
		doubles.assert(t)
	})

	t.Run("a network something else uses stays, with the daemon's reason", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		inUse := errors.New("error while removing network: network demo_default has active endpoints")
		left := model.ComposeProjectResources{Networks: []string{demoNetwork}}
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
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))
		doubles.expectDown(errors.New("daemon gone"))

		// Act
		_, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))

		// Assert
		require.ErrorContains(t, err, "daemon gone")
		doubles.assert(t)
	})
}

// The tags of this arcave project's compose project come off, of no other origin; a tag that will
// not come off is a notice, one already gone nothing.
func TestDropped_RemoveRestoredLayers(t *testing.T) {
	tag := func(n string) model.ImageRef { return model.ImageRef("arcave/demo-db-1-postgres:sp-" + n) }
	origin := model.LayerOrigin{ArcaveProject: demoArcaveProject, ComposeProject: demoProject}

	t.Run("only this compose project's", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, model.ComposeProjectResources{})
		ctx := zerolog.Nop().WithContext(context.Background())
		dropped, err := surveyed.Drop(ctx)
		require.NoError(t, err)
		mock.InOrder(
			doubles.tags.On("ListLayerImages", mock.Anything).Return([]model.TaggedImage{
				{Ref: tag("1"), Origin: origin},
				{Ref: tag("2"), Origin: model.LayerOrigin{ArcaveProject: demoArcaveProject, ComposeProject: "other"}},
				{Ref: tag("3"), Origin: model.LayerOrigin{ArcaveProject: "blog", ComposeProject: demoProject}},
				{Ref: tag("4")},
				{Ref: tag("5"), Origin: origin},
				{Ref: tag("6"), Origin: origin},
			}, nil).Once(),
			doubles.tags.On("RemoveImageRef", mock.Anything, tag("1")).Return(nil).Once(),
			doubles.tags.On("RemoveImageRef", mock.Anything, tag("5")).
				Return(fmt.Errorf("remove image reference: %w", model.ErrImageRefNotFound)).Once(),
			doubles.tags.On("RemoveImageRef", mock.Anything, tag("6")).
				Return(errors.New("image is used by container c0ffee")).Once(),
		)

		// Act
		removed, notices := dropped.RemoveRestoredLayers(ctx)

		// Assert
		assert.Equal(t, []model.ImageRef{tag("1")}, removed)
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0].Message, string(tag("6")))
		doubles.assert(t)
	})

	t.Run("an unreadable listing is a notice", func(t *testing.T) {
		// Arrange
		svc, doubles := newDropProvider()
		surveyed := survey(t, svc, doubles, model.ComposeProjectResources{})
		ctx := zerolog.Nop().WithContext(context.Background())
		dropped, err := surveyed.Drop(ctx)
		require.NoError(t, err)
		doubles.tags.On("ListLayerImages", mock.Anything).Return(nil, errors.New("daemon gone")).Once()

		// Act
		removed, notices := dropped.RemoveRestoredLayers(ctx)

		// Assert
		assert.Equal(t, []model.ImageRef{}, removed)
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0].Message, "daemon gone")
		doubles.assert(t)
	})
}

// The dropped compose project is built on as one that owned nothing and orphaned nothing; a
// savepoint another provider prepared is refused.
func TestDropped_Vacant(t *testing.T) {
	// Arrange
	svc, doubles := newDropProvider()
	surveyed := survey(t, svc, doubles, model.ComposeProjectResources{})
	dropped, err := surveyed.Drop(zerolog.Nop().WithContext(context.Background()))
	require.NoError(t, err)

	// Act
	vacant, err := dropped.Vacant(preparedSavepoint{project: demoTargetSpec})
	_, foreignErr := dropped.Vacant(foreignPrepared{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, vacantWorkload{
		service:              svc,
		anonymousVolumeNames: map[string]string{},
		ownedBefore:          map[string]bool{},
		downProject:          demoTargetSpec,
	}, vacant)
	require.ErrorIs(t, foreignErr, errForeignPrepared)
}

// A daemon the build cannot run on refuses the prepare, before anything is loaded.
func TestSurveyed_PrepareSavepoint_RefusesAnOldDaemon(t *testing.T) {
	// Arrange: the repository carries no expectation, so a load fails the test.
	svc, doubles := newDropProvider()
	svc.daemonVersion, svc.minDaemonMajor = "24.0.7", 25
	surveyed := survey(t, svc, doubles, resourcesOf(map[string]string{}))

	// Act
	_, err := surveyed.PrepareSavepoint(zerolog.Nop().WithContext(context.Background()), demoRecord().ID,
		provider.PrepareOptions{Deployment: demoDeployment(demoRecord())})

	// Assert
	require.ErrorIs(t, err, model.ErrUnsupportedDaemon)
}

// foreignPrepared is a prepared savepoint no compose provider handed out.
type foreignPrepared struct{}

func (foreignPrepared) Summary() provider.PreparedSummary { return provider.PreparedSummary{} }
