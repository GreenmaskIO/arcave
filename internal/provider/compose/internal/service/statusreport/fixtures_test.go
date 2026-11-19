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
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// demoArcaveProject is the arcave project the deployment labels name.
const demoArcaveProject model.ArcaveProjectName = "demo-arcave"

const demoService = "db"

var (
	statusCheckedAt = time.Date(2026, 11, 7, 17, 14, 0, 0, time.UTC)
	statusStarted   = time.Date(2026, 11, 7, 14, 2, 30, 0, time.UTC)
	// statusDeployed holds db-1's writable layer.
	statusDeployed = composemodel.Savepoint{
		Header: model.Savepoint{
			ID:          "019e843e-1650-7000-8000-000000000000",
			Name:        "before-migration",
			Description: "the schema before 0042",
			CreatedAt:   time.Date(2026, 11, 6, 19, 41, 0, 0, time.UTC),
		},
		Detail: composemodel.SavepointDetail{
			WritableLayers: []composemodel.WritableLayer{{Service: demoService, Replica: 1}},
		},
	}
	statusNewer = composemodel.Savepoint{Header: model.Savepoint{
		ID:        "019e9000-0000-7000-8000-000000000000",
		Name:      "after-fix",
		CreatedAt: time.Date(2026, 11, 7, 16, 14, 0, 0, time.UTC),
	}}
)

// newStatusDeployment is a rollback to savepoint from the default session, one replica of
// demoService recorded.
func newStatusDeployment(savepoint composemodel.Savepoint) composemodel.Deployment {
	return composemodel.Deployment{
		SavepointDeployment: model.SavepointDeployment{
			DeployedAt:    time.Date(2026, 11, 7, 14, 2, 0, 0, time.UTC),
			ArcaveProject: demoArcaveProject,
			Session:       model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
			Savepoint: model.DeployedSavepoint{
				ID:               savepoint.Header.ID,
				Name:             savepoint.Header.Name,
				CreatedAt:        savepoint.Header.CreatedAt,
				IsSafetySnapshot: savepoint.Header.IsSafetySnapshot(),
			},
		},
		ServiceReplicas: map[string][]int{demoService: {1}},
	}
}

// observedDeployment is a replica's deployment as Observe reads it off its labels: none, one, or
// labels that do not parse.
type observedDeployment struct {
	deployment *composemodel.Deployment
	err        error
}

// unreadableDeployment is a replica whose deployment labels do not parse.
var unreadableDeployment = observedDeployment{err: errors.New("bad time")}

// deployedBy is a replica built by deployment.
func deployedBy(deployment composemodel.Deployment) observedDeployment {
	return observedDeployment{deployment: &deployment}
}

// newStatusContainer is replica number of service as the daemon reports it, carrying deployed; of
// state it takes the state and the times.
func newStatusContainer(
	service string, number int, deployed observedDeployment, state composemodel.ObservedReplica,
) composemodel.ObservedReplica {
	return composemodel.ObservedReplica{
		Number:        number,
		Container:     fmt.Sprintf("demo-%s-%d", service, number),
		ContainerID:   fmt.Sprintf("%s-%d-id", service, number),
		Deployment:    deployed.deployment,
		DeploymentErr: deployed.err,
		State:         state.State,
		CreatedAt:     &statusStarted,
		StartedAt:     state.StartedAt,
		FinishedAt:    state.FinishedAt,
	}
}

func runningState() composemodel.ObservedReplica {
	return runningStateAt(statusStarted)
}

// runningStateAt is a replica running since started.
func runningStateAt(started time.Time) composemodel.ObservedReplica {
	return composemodel.ObservedReplica{
		State:     composemodel.ReplicaStateRunning,
		StartedAt: &started,
	}
}

// newRunningStatusReplica is newStatusContainer of a running replica as a status reports it, its
// times left out.
func newRunningStatusReplica(service string, number int, inSavepoint, inDeployment bool) composemodel.ReplicaStatus {
	return composemodel.ReplicaStatus{
		Number:       number,
		Container:    fmt.Sprintf("demo-%s-%d", service, number),
		ContainerID:  fmt.Sprintf("%s-%d-id", service, number),
		State:        composemodel.ReplicaStateRunning,
		InDeployment: inDeployment,
		InSavepoint:  inSavepoint,
	}
}

func withReplicaState(replica composemodel.ReplicaStatus, state composemodel.ReplicaState) composemodel.ReplicaStatus {
	replica.State = state
	return replica
}

// withoutReplicaTimes drops the times every replica's container carries.
func withoutReplicaTimes(services []composemodel.ServiceStatus) []composemodel.ServiceStatus {
	for i := range services {
		for j := range services[i].Replicas {
			services[i].Replicas[j].CreatedAt = nil
			services[i].Replicas[j].StartedAt = nil
		}
	}
	return services
}

// assertNotice asserts a notice of code carries message.
func assertNotice(t *testing.T, notices []model.Notice, code model.NoticeCode, message string) {
	t.Helper()
	assert.True(t, slices.ContainsFunc(notices, func(notice model.Notice) bool {
		return notice.Code == code && notice.Message == message
	}), "no %s notice %q among %v", code, message, notices)
}
