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

package statusreport

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

func TestFindLatestDeployment(t *testing.T) {
	deployment := newStatusDeployment(statusDeployed)
	earlier := newStatusDeployment(statusNewer)
	earlier.DeployedAt = deployment.DeployedAt.Add(-time.Hour)
	tests := []struct {
		name     string
		replicas []composemodel.ObservedReplica
		want     *model.SavepointDeployment
	}{
		{name: "no container"},
		{name: "no container a rollback built", replicas: []composemodel.ObservedReplica{
			newStatusContainer(demoService, 1, observedDeployment{}, runningState()),
		}},
		{name: "the latest of several, arcave's part alone", replicas: []composemodel.ObservedReplica{
			newStatusContainer(demoService, 1, deployedBy(earlier), runningState()),
			newStatusContainer(demoService, 2, deployedBy(deployment), runningState()),
		}, want: &deployment.SavepointDeployment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			observed := newObserved(tt.replicas...)

			// Act
			got := FindLatestDeployment(observed)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewReport(t *testing.T) {
	var (
		started      = statusCheckedAt.Add(-(3*time.Hour + 11*time.Minute + 30*time.Second))
		finished     = statusCheckedAt.Add(-(4*time.Minute + 30*time.Second))
		baseline     = newStatusDeployment(statusDeployed)
		twoReplicas  = newStatusDeployment(statusDeployed)
		twoLayers    = statusDeployed
		leftOut      = statusDeployed
		recreatedAt2 = func(deployed observedDeployment) []composemodel.ObservedReplica {
			second := newStatusContainer(demoService, 2, deployed, runningStateAt(started))
			second.CreatedAt = &started
			second.Replaces = "demo-db-2"
			return []composemodel.ObservedReplica{
				newStatusContainer(demoService, 1, deployedBy(twoReplicas), runningStateAt(started)),
				second,
			}
		}
	)
	twoReplicas.ServiceReplicas = map[string][]int{demoService: {1, 2}}
	twoLayers.Detail = composemodel.SavepointDetail{WritableLayers: []composemodel.WritableLayer{
		{Service: demoService, Replica: 1},
		{Service: demoService, Replica: 2},
	}}
	leftOut.Detail = composemodel.SavepointDetail{NotCovered: &composemodel.NotCovered{
		Mounts: []composemodel.SkippedVolume{
			{Service: demoService, Target: "/cache", Volume: "demo_cache", Reason: composemodel.SkipReasonExcluded},
			{Service: demoService, Target: "/run", Reason: composemodel.SkipReasonTmpfs},
		},
		WritableLayers: []composemodel.SkippedWritableLayer{{Service: demoService, Replica: 1}},
	}}
	tests := []struct {
		name     string
		replicas []composemodel.ObservedReplica
		deployed *composemodel.Savepoint
		verify   func(t *testing.T, report Report)
	}{
		{
			name:     "a rollback deployed every container",
			replicas: []composemodel.ObservedReplica{newStatusContainer(demoService, 1, deployedBy(baseline), runningStateAt(started))},
			deployed: &statusDeployed,
			verify: func(t *testing.T, report Report) {
				assert.Equal(t, composemodel.ComposeProjectStateRunning, report.State)
				assert.Equal(t, composemodel.DeploymentSourceSavepoint, report.Detail.Source)
				assert.Zero(t, report.Detail.Replaced)
				assert.Empty(t, report.Notices)
			},
		},
		{
			name:     "a container recreated by compose outside arcave",
			replicas: recreatedAt2(observedDeployment{}),
			deployed: &twoLayers,
			verify: func(t *testing.T, report Report) {
				assert.Equal(t, composemodel.DeploymentSourceMixed, report.Detail.Source)
				assert.Equal(t, 1, report.Detail.Replaced, "the replica the notice names, and no other")
				assert.Equal(t, composemodel.ComposeProjectStateRunning, report.State)
				assertNotice(t, report.Notices, composemodel.NoticeCodeReplicaRecreated, "db-2 was recreated outside arcave 3h 11m ago "+
					"(compose replaced demo-db-2): the writable layer the rollback restored is gone; volumes are untouched")
			},
		},
		{
			name:     "a replica whose labels do not parse is not called recreated",
			replicas: recreatedAt2(unreadableDeployment),
			deployed: &twoLayers,
			verify: func(t *testing.T, report Report) {
				assert.Zero(t, report.Detail.Replaced, "whether the deployment built it is unknown")
				require.Len(t, report.Notices, 1, "the unreadable labels alone: %v", report.Notices)
				assert.Equal(t, composemodel.NoticeCodeDeploymentLabelsUnreadable, report.Notices[0].Code)
			},
		},
		{
			name:     "a container recreated outside arcave with no record says nothing of its layer",
			replicas: recreatedAt2(observedDeployment{}),
			verify: func(t *testing.T, report Report) {
				assertNotice(t, report.Notices, composemodel.NoticeCodeReplicaRecreated,
					"db-2 was recreated outside arcave 3h 11m ago (compose replaced demo-db-2); volumes are untouched")
			},
		},
		{
			name:     "what the record left out",
			replicas: []composemodel.ObservedReplica{newStatusContainer(demoService, 1, deployedBy(baseline), runningStateAt(started))},
			deployed: &leftOut,
			verify: func(t *testing.T, report Report) {
				assert.Equal(t, []model.Notice{
					{Code: composemodel.NoticeCodeVolumeNotRestored,
						Message: "volume demo_cache was not in the savepoint: it holds what it held before the rollback"},
					{Code: composemodel.NoticeCodeLayerNotRestored,
						Message: "db-1's writable layer was not in the savepoint: it started fresh from its image"},
				}, report.Notices, "a tmpfs holds nothing to restore")
			},
		},
		{
			name: "every container stopped",
			replicas: []composemodel.ObservedReplica{newStatusContainer(demoService, 1, deployedBy(baseline),
				composemodel.ObservedReplica{State: composemodel.ReplicaStateExited, FinishedAt: &finished})},
			deployed: &statusDeployed,
			verify: func(t *testing.T, report Report) {
				assert.Equal(t, composemodel.ComposeProjectStateStopped, report.State)
				assert.Equal(t, []model.Notice{{Code: composemodel.NoticeCodeComposeProjectStopped, Message: "no container runs"}},
					report.Notices, "a clean exit adds nothing to a stopped project")
			},
		},
		{
			name: "nothing running",
			verify: func(t *testing.T, report Report) {
				assert.Equal(t, composemodel.ComposeProjectStateAbsent, report.State)
				assert.Empty(t, report.Detail.Source)
				encoded, err := json.Marshal(report.Detail)
				require.NoError(t, err)
				assert.JSONEq(t, `{"services":[]}`, string(encoded), "an empty list is [], never null")
				assert.NotNil(t, report.Notices, "none marshals as [], never null")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			observed := newObserved(tt.replicas...)

			// Act
			report := NewReport(observed, statusCheckedAt, tt.deployed)

			// Assert
			tt.verify(t, report)
		})
	}
}

// newObserved is a compose project of demoService's replicas; none with no replica.
func newObserved(replicas ...composemodel.ObservedReplica) composemodel.ObservedComposeProject {
	if len(replicas) == 0 {
		return composemodel.ObservedComposeProject{}
	}
	return composemodel.ObservedComposeProject{Services: []composemodel.ObservedService{{
		Name:     demoService,
		Replicas: replicas,
	}}}
}
