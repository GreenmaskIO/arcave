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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// statusFacts is everything a status's notices are read off.
type statusFacts struct {
	arcaveProject  model.ArcaveProjectName
	composeProject model.DockerComposeProjectName
	checkedAt      time.Time
	state          model.ComposeProjectState
	marker         *model.InflightMarker
	deployed       deployedContainers
	view           storeView
	services       []model.ServiceStatus
	hookRunners    []model.HookRunner
	unreadable     []model.StatusUnreadable
}

// newStatusNotices is one notice per thing that is wrong or worth knowing, the severe first. A
// notice states what is so; what to do about it is the documentation's, by its code.
func newStatusNotices(facts statusFacts) []model.Notice {
	var notices []model.Notice
	if facts.marker != nil {
		notices = append(notices, newInflightNotice(*facts.marker))
	}
	for _, unreadable := range facts.unreadable {
		notices = append(notices, newUnreadableNotice(unreadable))
	}
	notices = append(notices, facts.deployed.notices...)
	notices = append(notices, facts.view.notices...)
	if deployment := facts.deployed.primary; deployment != nil {
		notices = append(notices, newDeploymentNotices(facts, *deployment)...)
	}
	for _, service := range facts.services {
		for _, replica := range service.Replicas {
			notices = append(notices, newReplicaNotices(facts, service.Name, replica)...)
		}
	}
	for _, runner := range facts.hookRunners {
		if notice, failed := newHookRunnerNotice(runner, facts.checkedAt); failed {
			notices = append(notices, notice)
		}
	}
	if facts.state == model.ComposeProjectStateStopped {
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeComposeProjectStopped,
			Message: "no container runs",
		})
	}
	return slices.Concat([]model.Notice{}, model.SortSevereFirst(notices))
}

// newInflightNotice says a rollback is running or was interrupted, which a status cannot tell apart.
func newInflightNotice(marker model.InflightMarker) model.Notice {
	return model.Notice{
		Code: model.NoticeCodeRollbackPending,
		Message: fmt.Sprintf("a rollback to %s is in progress or was interrupted %s",
			marker.Target, formatInflightPhase(marker.Phase)),
		Severe: true,
	}
}

// formatInflightPhase says what a rollback was doing in phase.
func formatInflightPhase(phase model.InflightPhase) string {
	switch phase {
	case model.InflightRecreating:
		return "while recreating the containers"
	case model.InflightRestoring:
		return "while restoring the volumes"
	case model.InflightRestoreDone:
		return "while starting the services"
	default:
		return "and its revert failed"
	}
}

// newUnreadableNotice names what the status could not read: it shows the daemon alone for it.
func newUnreadableNotice(unreadable model.StatusUnreadable) model.Notice {
	switch unreadable.Source {
	case model.StatusUnreadableStore:
		return model.Notice{
			Code:    model.NoticeCodeStoreUnreadable,
			Message: "the store could not be read: " + unreadable.Error + "; showing the daemon alone",
		}
	case model.StatusUnreadableSession:
		return model.Notice{
			Code:    model.NoticeCodeSessionUnresolved,
			Message: "the current session could not be resolved: " + unreadable.Error,
		}
	default:
		return model.Notice{
			Code:    model.NoticeCodeInflightMarkerUnreadable,
			Message: "the inflight marker could not be read: " + unreadable.Error,
		}
	}
}

// newUnreadableLabelsNotice names a container whose deployment labels do not parse: another
// arcave's, or edited by hand.
func newUnreadableLabelsNotice(service string, replica model.Container, err error) model.Notice {
	version := ""
	if written := replica.Labels[string(model.SystemLabelDeploymentArcaveVersion)]; written != "" {
		version = " (written by arcave " + written + ")"
	}
	return model.Notice{
		Code: model.NoticeCodeDeploymentLabelsUnreadable,
		Message: fmt.Sprintf("%s carries unreadable deployment labels%s: %s; read as deployed by none",
			formatStatusReplica(service, replica.Number), version, err),
	}
}

// newOtherArcaveProjectNotice says the deployment is another arcave project's, whose store this
// checkout does not open.
func newOtherArcaveProjectNotice(arcaveProject model.ArcaveProjectName) model.Notice {
	return model.Notice{
		Code:    model.NoticeCodeOtherArcaveProject,
		Message: fmt.Sprintf("deployed by arcave project %s, whose store this one does not read", arcaveProject),
	}
}

// newSessionGoneNotice says the deployed savepoint's session is removed.
func newSessionGoneNotice(session model.SessionRef) model.Notice {
	return model.Notice{
		Code:    model.NoticeCodeDeployedSessionGone,
		Message: fmt.Sprintf("session %s the rollback came from no longer exists, nor its savepoints", session),
	}
}

// newSavepointGoneNotice says the deployed savepoint is removed from its session.
func newSavepointGoneNotice(deployment model.SavepointDeployment) model.Notice {
	return model.Notice{
		Code:    model.NoticeCodeDeployedSavepointGone,
		Message: fmt.Sprintf("savepoint %s is no longer in session %s", deployment.Savepoint.ID, deployment.Session),
	}
}

// newDeploymentNotices are what the deployment itself says: another session than the one commands
// work in, a revert or a safety snapshot deployed, and what the savepoint did not put back.
func newDeploymentNotices(facts statusFacts, deployment model.SavepointDeployment) []model.Notice {
	var notices []model.Notice
	if current := facts.view.session; current != nil && current.ID != deployment.Session.ID &&
		deployment.ArcaveProject == facts.arcaveProject {
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeOtherSession,
			Message: fmt.Sprintf("running savepoint %s of session %s; arcave commands act on session %s",
				formatSavepointName(deployment.Savepoint.ID, deployment.Savepoint.Name),
				deployment.Session, formatResolvedSession(*current)),
		})
	}
	if notice, ok := newSafetySnapshotNotice(deployment); ok {
		notices = append(notices, notice)
	}
	return append(notices, newNotRestoredNotices(deployment.Manifest.NotRestored)...)
}

// newSafetySnapshotNotice says the deployment is a rollback's undo, not a savepoint a user took.
func newSafetySnapshotNotice(deployment model.SavepointDeployment) (model.Notice, bool) {
	switch restoring := deployment.Savepoint.SafetySnapshotFor; {
	case deployment.Origin == model.DeploymentOriginRevert:
		return model.Notice{
			Code: model.NoticeCodeReverted,
			Message: fmt.Sprintf("the rollback to %s failed and was reverted to the state before it",
				formatSavepointName(deployment.RevertedFrom.ID, deployment.RevertedFrom.Name)),
		}, true
	case !deployment.Savepoint.IsSafetySnapshot():
		return model.Notice{}, false
	case restoring == "":
		return model.Notice{
			Code:    model.NoticeCodeSafetySnapshotDeployed,
			Message: "running the safety snapshot of a rollback",
		}, true
	default:
		return model.Notice{
			Code:    model.NoticeCodeSafetySnapshotDeployed,
			Message: fmt.Sprintf("running the safety snapshot of the rollback to %s", restoring),
		}, true
	}
}

// newNotRestoredNotices say what the deployed savepoint left out, so the rollback did not put back.
func newNotRestoredNotices(notRestored model.DeploymentNotRestored) []model.Notice {
	var notices []model.Notice
	for _, mount := range notRestored.Mounts {
		var message string
		switch {
		case mount.Volume != "":
			message = "volume " + mount.Volume + " was not in the savepoint: it holds what it held before the rollback"
		case mount.RawType == string(model.VolumeTypeBind):
			message = fmt.Sprintf("%s %s was not in the savepoint: the host folder holds what it held before the rollback",
				mount.Service, mount.Target)
		default:
			message = fmt.Sprintf("%s %s was not in the savepoint: it came back empty",
				formatStatusReplica(mount.Service, mount.Replica), mount.Target)
		}
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeVolumeNotRestored,
			Message: message,
		})
	}
	for _, layer := range notRestored.WritableLayers {
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeLayerNotRestored,
			Message: fmt.Sprintf("%s's writable layer was not in the savepoint: it started fresh from its image",
				formatStatusReplica(layer.Service, layer.Replica)),
		})
	}
	if notRestored.Truncated {
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeNotRestoredTruncated,
			Message: "the deployment labels name only part of what the savepoint left out",
		})
	}
	return notices
}

// newReplicaNotices say what is wrong with one replica, or what happened to it since the rollback.
func newReplicaNotices(facts statusFacts, service string, replica model.ReplicaStatus) []model.Notice {
	var (
		name    = formatStatusReplica(service, replica.Number)
		notices []model.Notice
	)
	switch replica.State {
	case model.ReplicaStateMissing:
		return []model.Notice{{
			Code: model.NoticeCodeReplicaMissing,
			Message: fmt.Sprintf("%s is missing: savepoint %s recorded it", name,
				formatSavepointName(facts.deployed.primary.Savepoint.ID, facts.deployed.primary.Savepoint.Name)),
			Severe: true,
		}}
	case model.ReplicaStateExited:
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeReplicaExited,
			Message: fmt.Sprintf("%s exited with code %d%s%s", name, replica.ExitCode, formatExitCause(replica),
				formatStatusTime(replica.FinishedAt, facts.checkedAt)),
			Severe: replica.ExitCode != 0,
		})
	case model.ReplicaStateDead:
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeReplicaDead,
			Message: name + " is dead",
			Severe:  true,
		})
	case model.ReplicaStateRemoving:
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeReplicaRemoving,
			Message: name + " is being removed",
			Severe:  true,
		})
	case model.ReplicaStateRestarting:
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeReplicaRestarting,
			Message: fmt.Sprintf("%s is restarting (%d restarts)", name, replica.RestartCount),
			Severe:  true,
		})
	case model.ReplicaStateCreated:
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeReplicaCreated,
			Message: name + " was created but never started",
			Severe:  true,
		})
	case model.ReplicaStatePaused:
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeReplicaPaused,
			Message: name + " is paused",
		})
	case model.ReplicaStateRunning:
		if replica.RestartCount > 0 {
			notices = append(notices, model.Notice{
				Code:    model.NoticeCodeReplicaRestarted,
				Message: fmt.Sprintf("%s restarted %d times since it was created", name, replica.RestartCount),
			})
		}
	}
	if replica.Health == model.ReplicaHealthUnhealthy {
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeReplicaUnhealthy,
			Message: fmt.Sprintf("%s is unhealthy: %d failing checks", name, replica.FailingChecks),
			Severe:  true,
		})
	}
	if notice, ok := newDriftNotice(facts, service, replica); ok {
		notices = append(notices, notice)
	}
	return notices
}

// formatExitCause says what an exit code means where the daemon tells: out of memory, or a signal.
func formatExitCause(replica model.ReplicaStatus) string {
	const (
		exitKilled     = 128 + 9
		exitTerminated = 128 + 15
	)
	switch {
	case replica.OOMKilled:
		return " (out of memory)"
	case replica.ExitCode == exitKilled:
		return " (killed)"
	case replica.ExitCode == exitTerminated:
		return " (terminated)"
	default:
		return ""
	}
}

// newDriftNotice says a replica is not what the rollback built: recreated outside arcave since,
// or added after it.
func newDriftNotice(facts statusFacts, service string, replica model.ReplicaStatus) (model.Notice, bool) {
	deployment := facts.deployed.primary
	if deployment == nil || replica.InDeployment {
		return model.Notice{}, false
	}
	name := formatStatusReplica(service, replica.Number)
	if !replica.InSavepoint {
		return model.Notice{
			Code: model.NoticeCodeReplicaAdded,
			Message: fmt.Sprintf("%s is not in savepoint %s: added after the rollback", name,
				formatSavepointName(deployment.Savepoint.ID, deployment.Savepoint.Name)),
		}, true
	}
	var message strings.Builder
	fmt.Fprintf(&message, "%s was recreated outside arcave%s", name, formatStatusTime(replica.CreatedAt, facts.checkedAt))
	if replica.Replaces != "" {
		fmt.Fprintf(&message, " (compose replaced %s)", replica.Replaces)
	}
	if slices.Contains(deployment.Manifest.Services[service].Layers, replica.Number) {
		message.WriteString(": the writable layer the rollback restored is gone")
	}
	message.WriteString("; volumes are untouched")
	return model.Notice{
		Code:    model.NoticeCodeReplicaRecreated,
		Message: message.String(),
	}, true
}

// newHookRunnerNotice says a service's lifecycle hook failed; failed is false for one that did not.
func newHookRunnerNotice(runner model.HookRunner, checkedAt time.Time) (_ model.Notice, failed bool) {
	state := runner.Container.Inspect.State
	if state == nil || state.Running || state.ExitCode == 0 {
		return model.Notice{}, false
	}
	return model.Notice{
		Code: model.NoticeCodeHookFailed,
		Message: fmt.Sprintf("the %s hook %s of %s failed with code %d%s; its container is %s",
			runner.Hook, runner.Index, runner.Service, state.ExitCode,
			formatStatusTime(parseDaemonTime(state.FinishedAt), checkedAt), runner.Container.Name),
		Severe: true,
	}, true
}

// formatStatusReplica names a replica as compose numbers it: service-n.
func formatStatusReplica(service string, number int) string {
	return service + "-" + strconv.Itoa(number)
}

// formatResolvedSession names the session the user works in, by its name or its id.
func formatResolvedSession(session model.ResolvedSession) string {
	return model.SessionRef{
		ID:   session.ID,
		Name: session.Name,
	}.String()
}

// formatSavepointName names a savepoint by its name and id, by its id when it has no name.
func formatSavepointName(id model.SavepointID, name string) string {
	if name == "" {
		return id.String()
	}
	return name + " (" + id.String() + ")"
}

// formatStatusTime is " <age> ago" at checkedAt; empty for no time. An age reads the same in every
// zone the result is shown in.
func formatStatusTime(t *time.Time, checkedAt time.Time) string {
	if t == nil {
		return ""
	}
	return " " + model.FormatAge(checkedAt.Sub(*t)) + " ago"
}
