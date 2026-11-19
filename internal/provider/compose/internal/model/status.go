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

package model

import (
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// ComposeProjectState sums a compose project up in one word, the first that applies in the order
// below; a status shows it as the workload's state.
type ComposeProjectState string

const (
	// ComposeProjectStateAbsent is a compose project with no container.
	ComposeProjectStateAbsent ComposeProjectState = "absent"
	// ComposeProjectStateStopped is one no container of runs, and one at least is stopped, a
	// completed job aside, with none missing, paused, restarting, dead or being removed.
	ComposeProjectStateStopped ComposeProjectState = "stopped"
	// ComposeProjectStateDegraded is one a replica of is missing, failing or unhealthy: exited with a
	// code other than 0, restarting, never started, paused, dead or being removed.
	ComposeProjectStateDegraded ComposeProjectState = "degraded"
	// ComposeProjectStateStarting is one nothing is wrong with but a healthcheck still starting.
	ComposeProjectStateStarting ComposeProjectState = "starting"
	// ComposeProjectStateRunning is one whose every replica runs, healthy where it is checked, or
	// exited 0: a status cannot tell a finished job no service waits on from a server stopped by
	// hand, and a notice names each.
	ComposeProjectStateRunning ComposeProjectState = "running"
)

// DeploymentSource says what built the containers of a compose project.
type DeploymentSource string

const (
	// DeploymentSourceSavepoint is every container built by one rollback.
	DeploymentSourceSavepoint DeploymentSource = "savepoint"
	// DeploymentSourceMixed is some containers built by a rollback, others since by something else.
	DeploymentSourceMixed DeploymentSource = "mixed"
	// DeploymentSourceCompose is no container a rollback built: compose started them from a file.
	DeploymentSourceCompose DeploymentSource = "compose"
)

// ReplicaState is one replica in one word, as the daemon reports it.
type ReplicaState string

const (
	ReplicaStateRunning ReplicaState = "running"
	// ReplicaStateCompleted is a job's replica that exited 0: its service is one another waits on
	// to complete successfully.
	ReplicaStateCompleted  ReplicaState = "completed"
	ReplicaStateExited     ReplicaState = "exited"
	ReplicaStateRestarting ReplicaState = "restarting"
	// ReplicaStateCreated is a replica created and never started.
	ReplicaStateCreated  ReplicaState = "created"
	ReplicaStatePaused   ReplicaState = "paused"
	ReplicaStateRemoving ReplicaState = "removing"
	ReplicaStateDead     ReplicaState = "dead"
	// ReplicaStateMissing is a replica the deployed savepoint recorded that the daemon does not hold.
	ReplicaStateMissing ReplicaState = "missing"
)

// ReplicaHealth is a replica's healthcheck; empty for one with none.
type ReplicaHealth string

const (
	ReplicaHealthStarting  ReplicaHealth = "starting"
	ReplicaHealthHealthy   ReplicaHealth = "healthy"
	ReplicaHealthUnhealthy ReplicaHealth = "unhealthy"
)

// The notices a status of a compose project reports beside arcave's own, one code per condition; a
// code is stable, its message is not.
const (
	NoticeCodeDeploymentLabelsUnreadable model.NoticeCode = "deployment-labels-unreadable"
	NoticeCodeVolumeNotRestored          model.NoticeCode = "volume-not-restored"
	NoticeCodeLayerNotRestored           model.NoticeCode = "layer-not-restored"
	NoticeCodeReplicaMissing             model.NoticeCode = "replica-missing"
	NoticeCodeReplicaExited              model.NoticeCode = "replica-exited"
	NoticeCodeReplicaDead                model.NoticeCode = "replica-dead"
	NoticeCodeReplicaRemoving            model.NoticeCode = "replica-removing"
	NoticeCodeReplicaRestarting          model.NoticeCode = "replica-restarting"
	NoticeCodeReplicaCreated             model.NoticeCode = "replica-created"
	NoticeCodeReplicaPaused              model.NoticeCode = "replica-paused"
	NoticeCodeReplicaRestarted           model.NoticeCode = "replica-restarted"
	NoticeCodeReplicaUnhealthy           model.NoticeCode = "replica-unhealthy"
	NoticeCodeReplicaRecreated           model.NoticeCode = "replica-recreated"
	NoticeCodeReplicaAdded               model.NoticeCode = "replica-added"
	NoticeCodeComposeProjectStopped      model.NoticeCode = "compose-project-stopped"
)

// StatusDetail is the compose provider's body of a status: what built the containers, and every
// service. Its json tags are the wire contract of a status's detail.
type StatusDetail struct {
	// Source is empty for a compose project with no container.
	Source DeploymentSource `json:"source,omitempty"`
	// Replaced counts the replicas the deployed savepoint recorded that were recreated outside
	// arcave since.
	Replaced int             `json:"replaced,omitempty"`
	Services []ServiceStatus `json:"services"`
}

// ServiceStatus is one service: the deployed savepoint's, the daemon's, or both.
type ServiceStatus struct {
	Name string `json:"name"`
	// State is its replicas' worst; completed only when every replica is.
	State ReplicaState `json:"state"`
	// Health is its replicas' worst healthcheck; empty when none runs one.
	Health ReplicaHealth `json:"health,omitempty"`
	// ReplicasExpected counts the replicas the deployed savepoint recorded; 0 with none deployed.
	ReplicasExpected int `json:"replicas_expected,omitempty"`
	// ReplicasUp counts the replicas running, or completed for a job.
	ReplicasUp int             `json:"replicas_up"`
	Replicas   []ReplicaStatus `json:"replicas"`
}

// ReplicaStatus is one replica, a container the daemon holds or one the deployment misses.
type ReplicaStatus struct {
	Number       int           `json:"number"`
	Container    string        `json:"container,omitempty"`
	ContainerID  string        `json:"container_id,omitempty"`
	State        ReplicaState  `json:"state"`
	Health       ReplicaHealth `json:"health,omitempty"`
	ExitCode     int           `json:"exit_code"`
	OOMKilled    bool          `json:"oom_killed"`
	RestartCount int           `json:"restart_count"`
	// FailingChecks is the healthcheck's failing streak.
	FailingChecks int        `json:"failing_checks,omitempty"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	// Replaces is the container compose recreated this one over.
	Replaces string `json:"replaces,omitempty"`
	// InDeployment marks a container the deployment built; InSavepoint a replica the deployed
	// savepoint recorded. In the savepoint alone: recreated outside arcave; in neither: added since.
	InDeployment bool            `json:"in_deployment"`
	InSavepoint  bool            `json:"in_savepoint"`
	Ports        []PublishedPort `json:"ports,omitempty"`
}

// PublishedPort is one port a replica publishes on the host.
type PublishedPort struct {
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	// HostIP is empty when the daemon reported none.
	HostIP   string `json:"host_ip,omitempty"`
	HostPort int    `json:"host_port"`
}

// ObservedComposeProject is a compose project's replicas as the daemon runs them: what Observe
// reads for a status, nothing they mount or run on.
type ObservedComposeProject struct {
	// Services are the services the daemon holds a replica of, sorted by name.
	Services []ObservedService
}

// ObservedService is one service's replicas.
type ObservedService struct {
	Name string
	// Job is a service another waits on to complete successfully: its exit 0 is its success.
	Job bool
	// Replicas are sorted by Number.
	Replicas []ObservedReplica
}

// ObservedReplica is one container as the daemon reports it. State is never completed or missing:
// whether an exit is a job's success, and what is missing, are a status's to judge.
type ObservedReplica struct {
	Number      int
	Container   string
	ContainerID string
	// Deployment is the rollback build the container carries; nil for none or unreadable.
	Deployment *Deployment
	// DeploymentErr is why the container's deployment does not read; nil when it does or it has none.
	DeploymentErr error
	State         ReplicaState
	Health        ReplicaHealth
	ExitCode      int
	OOMKilled     bool
	RestartCount  int
	FailingChecks int
	CreatedAt     *time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	// Replaces is the container this one was recreated over; empty for one created anew.
	Replaces string
	Ports    []PublishedPort
}
