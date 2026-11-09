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
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNewDeployedReplicas(t *testing.T) {
	// Arrange
	scaleTwo, scaleZero := 2, 0
	project := &types.Project{Name: demoProject, Services: types.Services{
		"db":      {Name: "db"},
		"web":     {Name: "web", Scale: &scaleTwo},
		"app":     {Name: "app"},
		"migrate": {Name: "migrate"},
		"idle":    {Name: "idle", Scale: &scaleZero},
	}}
	recorded := []recordedReplica{
		{service: "db", number: 3},
		{service: "db", number: 1},
		{service: "app", number: 1},
	}

	// Act
	replicas := newDeployedReplicas(project, recorded)

	// Assert
	assert.Equal(t, map[string][]int{
		"db":      {1, 3},
		"web":     {1, 2},
		"app":     {1},
		"migrate": {1},
	}, replicas, "a recorded gap stays, a service recorded with no replica runs at its scale, one scaled to none is left out")
}

func TestWithDeploymentLabels(t *testing.T) {
	// Arrange
	project := &types.Project{
		Name: demoProject,
		Services: types.Services{
			demoService: {Name: demoService, Labels: types.Labels{"keep": "me"}},
		},
		Volumes:  types.Volumes{"db": {Name: demoVolumeName}},
		Networks: types.Networks{"default": {Name: "demo_default"}},
	}

	// Act
	labeled := withDeploymentLabels(project, map[string]string{string(model.SystemLabelDeploymentSavepointID): "id"})

	// Assert
	assert.Equal(t, types.Labels{"keep": "me", string(model.SystemLabelDeploymentSavepointID): "id"},
		labeled.Services[demoService].Labels)
	assert.Equal(t, types.Labels{"keep": "me"}, project.Services[demoService].Labels, "the project handed in is left as it is")
	assert.Nil(t, labeled.Volumes["db"].Labels, "a volume's labels are in compose's config hash")
	assert.Nil(t, labeled.Networks["default"].Labels, "and so are a network's")
}

func TestService_buildFromSavepoint(t *testing.T) {
	record := demoRecord()
	failed := model.SavepointID("019e9000-0000-7000-8000-000000000000")
	session := model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}
	tests := []struct {
		name                 string
		savepoint            model.Savepoint
		markerTarget         model.SavepointID
		wantIsSafetySnapshot bool
	}{
		{
			name:         "a rollback deploys its savepoint",
			savepoint:    record,
			markerTarget: record.ID,
		},
		{
			name:                 "a revert deploys the safety snapshot, the marker naming the rollback it undid",
			savepoint:            demoSafetySnapshot(),
			markerTarget:         failed,
			wantIsSafetySnapshot: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: no volume to restore, so only the compose calls carry the deployment.
			target := &types.Project{
				Name: demoProject,
				Services: types.Services{
					"db":  {Name: "db", Image: demoBaseImage},
					"web": {Name: "web", Image: "nginx:1.27"},
				},
				Volumes:  types.Volumes{"db": {Name: demoVolumeName}},
				Networks: types.Networks{"default": {Name: "demo_default"}},
			}
			savepoint := tt.savepoint
			savepoint.Stats = nil
			var created, started *types.Project
			runner := &mockComposeRunner{}
			create := runner.On("Create", mock.Anything, mock.MatchedBy(matchDeploymentOf(target)), []string(nil)).
				Run(func(args mock.Arguments) { created = args.Get(1).(*types.Project) }).Return(nil).Once()
			start := runner.On("Start", mock.Anything, mock.MatchedBy(matchDeploymentOf(target)), time.Minute).
				Run(func(args mock.Arguments) { started = args.Get(1).(*types.Project) }).Return(nil).Once()
			mock.InOrder(create, start)
			inflight := &fakeInflight{}
			svc := New(Deps{
				ComposeRunner: runner,
				Inflight:      inflight,
				ArcaveProject: demoArcaveProject,
				Session:       model.DefaultSession,
			})
			ctx := zerolog.Nop().WithContext(context.Background())
			before := time.Now().UTC().Truncate(time.Second)

			// Act
			err := svc.buildFromSavepoint(ctx, savepoint, rollbackTarget{
				project: target,
				session: session,
			}, tt.markerTarget, "", time.Minute)

			// Assert
			require.NoError(t, err)
			require.NotNil(t, created)
			deployment, ok, err := model.ParseSavepointDeployment(created.Services["db"].Labels)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, demoArcaveProject, deployment.ArcaveProject)
			assert.Equal(t, session, deployment.Session)
			assert.Equal(t, model.DeployedSavepoint{
				ID:               savepoint.ID,
				Name:             savepoint.Name,
				CreatedAt:        savepoint.CreatedAt.UTC().Truncate(time.Second),
				IsSafetySnapshot: tt.wantIsSafetySnapshot,
			}, deployment.Savepoint)
			assert.Equal(t, map[string][]int{"db": {1}, "web": {1}}, deployment.ServiceReplicas)
			assert.False(t, deployment.DeployedAt.Before(before), "dated when it is built")
			assert.Equal(t, created.Services["db"].Labels, created.Services["web"].Labels,
				"every service carries the one deployment")
			assert.Equal(t, created, started, "the project started is the one created")
			assert.Nil(t, target.Services["db"].Labels, "the target the caller holds is left unlabelled")
			assert.Equal(t, tt.markerTarget, inflight.marker.Target)
			runner.AssertExpectations(t)
		})
	}
}

// matchDeploymentOf matches target with its every service labelled with a deployment, and its
// volumes and networks with none: their labels are in compose's config hash.
func matchDeploymentOf(target *types.Project) func(*types.Project) bool {
	return func(project *types.Project) bool {
		for _, volume := range project.Volumes {
			if hasDeploymentLabel(volume.Labels) {
				return false
			}
		}
		for _, network := range project.Networks {
			if hasDeploymentLabel(network.Labels) {
				return false
			}
		}
		return len(project.Services) == len(target.Services) &&
			assert.ObjectsAreEqual(target, withoutDeploymentLabels(project))
	}
}

func hasDeploymentLabel(labels types.Labels) bool {
	for key := range labels {
		if strings.HasPrefix(key, model.DeploymentLabelPrefix) {
			return true
		}
	}
	return false
}
