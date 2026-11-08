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
	"fmt"
	"time"
)

// ComposeProjectState sums a compose project up in one word, the first that applies in the order
// below.
type ComposeProjectState string

const (
	// ComposeProjectStateRollbackPending is a compose project an inflight marker names: a rollback
	// is running, or was interrupted.
	ComposeProjectStateRollbackPending ComposeProjectState = "rollback-pending"
	// ComposeProjectStateAbsent is a compose project with no container.
	ComposeProjectStateAbsent ComposeProjectState = "absent"
	// ComposeProjectStateStopped is one no container of runs, and one at least is stopped, a
	// completed job aside.
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

// DeploymentRecord says whether the store still holds the savepoint a rollback deployed.
type DeploymentRecord string

const (
	DeploymentRecordFound   DeploymentRecord = "found"
	DeploymentRecordMissing DeploymentRecord = "missing"
	// DeploymentRecordUnreadable is a store that could not be read.
	DeploymentRecordUnreadable DeploymentRecord = "unreadable"
)

// SessionResolution says what named the session a command works in.
type SessionResolution string

const (
	// SessionResolutionRef is a session the command named: --session, ARCAVE_SESSION, the MCP
	// argument or the server's pin.
	SessionResolutionRef SessionResolution = "ref"
	// SessionResolutionPointer is the current session `session set` named.
	SessionResolutionPointer SessionResolution = "pointer"
	// SessionResolutionDefault is the default session, no pointer being set.
	SessionResolutionDefault SessionResolution = "default"
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

// StatusUnreadableSource names what a status could not read.
type StatusUnreadableSource string

const (
	StatusUnreadableStore          StatusUnreadableSource = "store"
	StatusUnreadableSession        StatusUnreadableSource = "session"
	StatusUnreadableInflightMarker StatusUnreadableSource = "inflight-marker"
)

// The notices a status reports, one code per condition; a code is stable, its message is not.
const (
	NoticeCodeRollbackPending            NoticeCode = "rollback-pending"
	NoticeCodeStoreUnreadable            NoticeCode = "store-unreadable"
	NoticeCodeSessionUnresolved          NoticeCode = "session-unresolved"
	NoticeCodeInflightMarkerUnreadable   NoticeCode = "inflight-marker-unreadable"
	NoticeCodeDeploymentLabelsUnreadable NoticeCode = "deployment-labels-unreadable"
	NoticeCodeOtherArcaveProject         NoticeCode = "other-arcave-project"
	NoticeCodeDeployedSessionGone        NoticeCode = "deployed-session-gone"
	NoticeCodeDeployedSavepointGone      NoticeCode = "deployed-savepoint-gone"
	NoticeCodeOtherSession               NoticeCode = "other-session"
	NoticeCodeReverted                   NoticeCode = "reverted"
	NoticeCodeSafetySnapshotDeployed     NoticeCode = "safety-snapshot-deployed"
	NoticeCodeVolumeNotRestored          NoticeCode = "volume-not-restored"
	NoticeCodeLayerNotRestored           NoticeCode = "layer-not-restored"
	NoticeCodeNotRestoredTruncated       NoticeCode = "not-restored-truncated"
	NoticeCodeReplicaMissing             NoticeCode = "replica-missing"
	NoticeCodeReplicaExited              NoticeCode = "replica-exited"
	NoticeCodeReplicaDead                NoticeCode = "replica-dead"
	NoticeCodeReplicaRemoving            NoticeCode = "replica-removing"
	NoticeCodeReplicaRestarting          NoticeCode = "replica-restarting"
	NoticeCodeReplicaCreated             NoticeCode = "replica-created"
	NoticeCodeReplicaPaused              NoticeCode = "replica-paused"
	NoticeCodeReplicaRestarted           NoticeCode = "replica-restarted"
	NoticeCodeReplicaUnhealthy           NoticeCode = "replica-unhealthy"
	NoticeCodeReplicaRecreated           NoticeCode = "replica-recreated"
	NoticeCodeReplicaAdded               NoticeCode = "replica-added"
	NoticeCodeHookFailed                 NoticeCode = "hook-failed"
	NoticeCodeComposeProjectStopped      NoticeCode = "compose-project-stopped"
)

// StatusRequest carries what Status needs.
type StatusRequest struct {
	// Session names the session the user works in, as given; empty is the current session.
	Session string
}

// ComposeProjectStatus is what the daemon runs for a compose project, what built it, and where
// that stands against the store. Its json tags are the wire contract.
type ComposeProjectStatus struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	// CheckedAt is when the daemon was read; every age is counted from it.
	CheckedAt time.Time           `json:"checked_at"`
	State     ComposeProjectState `json:"state"`
	// Session is the session arcave commands work in; nil when it could not be resolved.
	Session *ResolvedSession `json:"session"`
	// Deployment is what built the containers; nil for a compose project with none.
	Deployment *DeploymentStatus `json:"deployment"`
	// LatestSavepoint, SavepointCount and NewerThanDeployed read the deployed savepoint's session
	// if the store holds it, else the current one; safety snapshots are not counted.
	LatestSavepoint *LatestSavepoint `json:"latest_savepoint"`
	SavepointCount  int              `json:"savepoint_count"`
	// NewerThanDeployed counts the savepoints taken after the deployed one; nil with none deployed.
	NewerThanDeployed *int            `json:"newer_than_deployed,omitempty"`
	Services          []ServiceStatus `json:"services"`
	// RollbackInFlight is the inflight marker of this compose project: a rollback running, or
	// interrupted, which a status does not tell apart.
	RollbackInFlight *RollbackInFlight `json:"rollback_in_flight"`
	// Unreadable is what could not be read; any makes the status partial.
	Unreadable []StatusUnreadable `json:"unreadable"`
	Notices    []Notice           `json:"notices"`
}

// ResolvedSession is the session arcave commands work in, and what named it.
type ResolvedSession struct {
	ID         SessionID         `json:"id"`
	Name       string            `json:"name"`
	ResolvedBy SessionResolution `json:"resolved_by"`
}

// DeploymentStatus is what built a compose project's containers. Under DeploymentSourceCompose it
// holds the source alone; otherwise the deployment most containers carry, as their labels spell it.
type DeploymentStatus struct {
	Source        DeploymentSource  `json:"source"`
	ID            DeploymentID      `json:"id,omitempty"`
	DeployedAt    *time.Time        `json:"deployed_at,omitempty"`
	Origin        DeploymentOrigin  `json:"origin,omitempty"`
	ArcaveProject ArcaveProjectName `json:"arcave_project,omitempty"`
	// RevertedFrom is the savepoint a failed rollback was restoring when Origin is a revert.
	RevertedFrom *SavepointRef `json:"reverted_from,omitempty"`
	// Session is the session the deployed savepoint was rolled back from.
	Session   *SessionRef              `json:"session,omitempty"`
	Savepoint *DeployedSavepointStatus `json:"savepoint,omitempty"`
	// Record says whether that session still holds the savepoint; empty when the store was not
	// read for it.
	Record DeploymentRecord `json:"record,omitempty"`
	// Host and ArcaveVersion name the host and the arcave that ran the rollback.
	Host          string `json:"host,omitempty"`
	ArcaveVersion string `json:"arcave_version,omitempty"`
}

// DeployedSavepointStatus is the savepoint a deployment was built from, as it was when it was.
type DeployedSavepointStatus struct {
	ID             SavepointID `json:"id"`
	Name           string      `json:"name,omitempty"`
	Description    string      `json:"description,omitempty"`
	CreatedAt      *time.Time  `json:"created_at,omitempty"`
	SafetySnapshot bool        `json:"safety_snapshot"`
	// SafetySnapshotFor is the savepoint the rollback a safety snapshot undoes restored.
	SafetySnapshotFor SavepointID      `json:"safety_snapshot_for,omitempty"`
	Restored          DeploymentCounts `json:"restored"`
	NotRestored       DeploymentCounts `json:"not_restored"`
}

// LatestSavepoint is the newest savepoint of a session's compose project.
type LatestSavepoint struct {
	Session   SessionID   `json:"session"`
	ID        SavepointID `json:"id"`
	Name      string      `json:"name,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
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
	Image         string     `json:"image,omitempty"`
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

// RollbackInFlight is a compose project's inflight marker as a status reports it.
type RollbackInFlight struct {
	Phase   InflightPhase `json:"phase"`
	Session SessionID     `json:"session"`
	Target  SavepointID   `json:"target"`
	Safety  SavepointID   `json:"safety,omitempty"`
	At      time.Time     `json:"at"`
	// Recover is the savepoint a rollback to recovers.
	Recover SavepointID `json:"recover"`
}

// StatusUnreadable is one thing a status could not read, and why.
type StatusUnreadable struct {
	Source StatusUnreadableSource `json:"source"`
	Error  string                 `json:"error"`
}

// FormatAge spells a duration the way a status shows an age: 45s, 3m, 3h 12m, 2d 4h.
func FormatAge(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
}
