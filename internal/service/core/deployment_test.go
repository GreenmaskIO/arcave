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
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

func TestNewDeploymentManifest(t *testing.T) {
	scaleTwo := 2
	project := &types.Project{Name: demoProject, Services: types.Services{
		"db":  {Name: "db"},
		"web": {Name: "web", Scale: &scaleTwo},
		"app": {Name: "app", DependsOn: types.DependsOnConfig{
			"migrate": {Condition: types.ServiceConditionCompletedSuccessfully},
			"db":      {Condition: types.ServiceConditionHealthy},
		}},
		"migrate": {Name: "migrate"},
	}}
	excludedCache := model.SkippedVolume{Volume: "demo_cache", Reason: model.SkipReasonExcluded}
	skippedLayer := model.SkippedWritableLayer{Service: "db", Replica: 3, Container: "demo-db-3", Reason: model.SkipReasonExcluded}
	savepoint := model.Savepoint{
		ID:    demoRecord().ID,
		Stats: []model.SnapshotStat{{Volume: demoVolume()}},
		WritableLayers: []model.WritableLayer{
			{Service: "db", Replica: 1},
			{Service: "app", Replica: 1},
		},
		NotCovered: &model.NotCovered{
			Mounts: []model.SkippedVolume{
				excludedCache,
				{Service: "db", Target: "/tmp", Reason: model.SkipReasonTmpfs},
			},
			WritableLayers: []model.SkippedWritableLayer{skippedLayer},
		},
	}

	// Act
	manifest := newDeploymentManifest(savepoint, project)

	// Assert
	assert.Equal(t, model.DeploymentManifest{
		Services: map[string]model.DeployedService{
			"db":      {Replicas: []int{1, 3}, Layers: []int{1}},
			"web":     {Replicas: []int{1, 2}},
			"app":     {Replicas: []int{1}, Layers: []int{1}},
			"migrate": {Replicas: []int{1}},
		},
		Restored: model.DeploymentCounts{Volumes: 1, WritableLayers: 2},
		LeftOut:  model.DeploymentCounts{Volumes: 1, WritableLayers: 1},
		NotRestored: model.DeploymentNotRestored{
			Mounts:         []model.SkippedVolume{excludedCache},
			WritableLayers: []model.SkippedWritableLayer{skippedLayer},
		},
	}, manifest, "a recorded gap stays, a service recorded with no replica runs at its scale, a tmpfs is no loss")
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
	labeled := withDeploymentLabels(project, map[string]string{string(model.SystemLabelDeploymentID): "id"})

	// Assert
	assert.Equal(t, types.Labels{"keep": "me", string(model.SystemLabelDeploymentID): "id"},
		labeled.Services[demoService].Labels)
	assert.Equal(t, types.Labels{"keep": "me"}, project.Services[demoService].Labels, "the project handed in is left as it is")
	assert.Nil(t, labeled.Volumes["db"].Labels, "a volume's labels are in compose's config hash")
	assert.Nil(t, labeled.Networks["default"].Labels, "and so are a network's")
}

func TestService_buildFromSavepoint(t *testing.T) {
	record := demoRecord()
	deployedAt := time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC)
	failed := model.Savepoint{ID: "019e9000-0000-7000-8000-000000000000", Name: "after-fix"}
	tests := []struct {
		name             string
		savepoint        model.Savepoint
		revertedFrom     *model.Savepoint
		wantOrigin       model.DeploymentOrigin
		wantRevertedFrom model.SavepointRef
		wantMarkerTarget model.SavepointID
	}{
		{
			name:             "a rollback deploys its savepoint",
			savepoint:        record,
			wantOrigin:       model.DeploymentOriginRollback,
			wantMarkerTarget: record.ID,
		},
		{
			name:         "a revert deploys the safety snapshot and names the rollback it undid",
			savepoint:    demoSafetySnapshot(),
			revertedFrom: &failed,
			wantOrigin:   model.DeploymentOriginRevert,
			wantRevertedFrom: model.SavepointRef{
				ID:   failed.ID,
				Name: failed.Name,
			},
			wantMarkerTarget: failed.ID,
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
				Session:       model.DefaultSession,
				SessionName:   model.DefaultSessionName,
				ArcaveProject: "app",
				RunnerInfo:    heartbeat.RunnerInfo{Host: "laptop", ArcaveVersion: "v0.1.0"},
				Now:           func() time.Time { return deployedAt },
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := svc.buildFromSavepoint(ctx, savepoint, tt.revertedFrom, rollbackTarget{project: target}, "", time.Minute)

			// Assert
			require.NoError(t, err)
			require.NotNil(t, created)
			deployment, ok, err := model.ParseSavepointDeployment(created.Services["db"].Labels)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, deployment.Session)
			assert.Equal(t, model.ArcaveProjectName("app"), deployment.ArcaveProject)
			assert.Equal(t, savepoint.ID, deployment.Savepoint.ID)
			assert.Equal(t, savepoint.Name, deployment.Savepoint.Name)
			assert.Equal(t, tt.wantOrigin, deployment.Origin)
			assert.Equal(t, tt.wantRevertedFrom, deployment.RevertedFrom)
			assert.Equal(t, "laptop", deployment.Host)
			assert.Equal(t, deployedAt, deployment.At)
			assert.Equal(t, created.Services["db"].Labels, created.Services["web"].Labels,
				"every service carries the one deployment")
			assert.Equal(t, created, started, "the project started is the one created")
			assert.Nil(t, target.Services["db"].Labels, "the target the caller holds is left unlabelled")
			assert.Equal(t, tt.wantMarkerTarget, inflight.marker.Target)
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
