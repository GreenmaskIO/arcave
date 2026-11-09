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

package status

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
	checkedAt time.Time
	// arcaveProject is the one the config names.
	arcaveProject model.ArcaveProjectName
	// ref is the request's --session, empty when none was given.
	ref         string
	state       model.ComposeProjectState
	marker      *model.InflightMarker
	deployments containerDeployments
	view        storeView
	services    []model.ServiceStatus
	unreadable  []model.StatusUnreadable
}

// newStatusNotices is one notice per thing that is wrong or worth knowing, the severe first. A
// notice states what is so; what to do about it is the documentation's, by its code.
func newStatusNotices(facts statusFacts) []model.Notice {
	var notices []model.Notice
	if facts.marker != nil {
		notices = append(notices, newInflightNotice(*facts.marker))
	}
	for _, unreadable := range facts.unreadable {
		notices = append(notices, newUnreadableNotice(unreadable, facts.ref))
	}
	for _, labels := range facts.deployments.unreadable {
		notices = append(notices, newUnreadableLabelsNotice(labels))
	}
	if deployment := facts.deployments.latest; deployment != nil {
		notices = append(notices, newDeploymentNotices(facts, *deployment)...)
	}
	for _, service := range facts.services {
		for _, replica := range service.Replicas {
			notices = append(notices, newReplicaNotices(facts, service.Name, replica)...)
		}
	}
	if facts.state == model.ComposeProjectStateStopped {
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeComposeProjectStopped,
			Message: "no container runs",
		})
	}
	// Non-nil, so none marshals as [], not null: slices.Concat of empty slices is nil.
	return append([]model.Notice{}, model.SortSevereFirst(notices)...)
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
	case model.InflightInconsistent:
		return "and its revert failed"
	default:
		return fmt.Sprintf("in phase %q", phase)
	}
}

// newUnreadableNotice names what the status could not read: it shows the daemon alone for it. ref
// is the request's --session, empty when none was given.
func newUnreadableNotice(unreadable model.StatusUnreadable, ref string) model.Notice {
	switch unreadable.Source {
	case model.StatusUnreadableStore:
		return model.Notice{
			Code:    model.NoticeCodeStoreUnreadable,
			Message: "the store could not be read: " + unreadable.Error + "; showing the daemon alone",
		}
	case model.StatusUnreadableSession:
		return model.Notice{
			Code:    model.NoticeCodeSessionUnresolved,
			Message: newSessionUnresolvedMessage(ref, unreadable.Error),
		}
	default:
		return model.Notice{
			Code:    model.NoticeCodeInflightMarkerUnreadable,
			Message: "the inflight marker could not be read: " + unreadable.Error,
		}
	}
}

// newSessionUnresolvedMessage names the session the status could not resolve: ref, or the current one.
func newSessionUnresolvedMessage(ref, cause string) string {
	if ref == "" {
		return "the current session could not be resolved: " + cause
	}
	return fmt.Sprintf("session %q could not be resolved: %s", ref, cause)
}

// newUnreadableLabelsNotice names a container whose deployment labels do not parse: edited by hand,
// or written by another arcave.
func newUnreadableLabelsNotice(labels unreadableLabels) model.Notice {
	return model.Notice{
		Code: model.NoticeCodeDeploymentLabelsUnreadable,
		Message: fmt.Sprintf("%s carries unreadable deployment labels: %s; read as deployed by none",
			formatStatusReplica(labels.service, labels.replica), labels.err),
	}
}

// newDeploymentNotices are what the deployment itself says: another arcave project or session than
// the one commands work in, a safety snapshot deployed, a savepoint the store no longer holds, and
// what the savepoint's record says it did not put back.
func newDeploymentNotices(facts statusFacts, deployment model.SavepointDeployment) []model.Notice {
	var (
		notices []model.Notice
		current = facts.view.session
	)
	switch {
	case deployment.ArcaveProject != facts.arcaveProject:
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeOtherArcaveProject,
			Message: fmt.Sprintf("running savepoint %s of arcave project %s; arcave commands act on arcave project %s",
				deployment.Savepoint,
				deployment.ArcaveProject, facts.arcaveProject),
		})
	case current != nil && current.ID != deployment.Session.ID:
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeOtherSession,
			Message: fmt.Sprintf("running savepoint %s of session %s; arcave commands act on session %s",
				deployment.Savepoint,
				deployment.Session, current.String()),
		})
	}
	if deployment.Savepoint.IsSafetySnapshot {
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeSafetySnapshotDeployed,
			Message: fmt.Sprintf("running %s, the safety snapshot of a rollback",
				deployment.Savepoint),
		})
	}
	switch {
	case facts.view.record == model.DeploymentRecordMissing:
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeDeployedSavepointGone,
			Message: fmt.Sprintf("savepoint %s is no longer in session %s",
				deployment.Savepoint, deployment.Session),
		})
	case facts.view.savepoint != nil && facts.view.savepoint.NotCovered != nil:
		notices = append(notices, newNotRestoredNotices(*facts.view.savepoint.NotCovered)...)
	}
	return notices
}

// newNotRestoredNotices say what the deployed savepoint's record left out, so the rollback did not
// put back: a mount a label or the config excluded, and a writable layer.
func newNotRestoredNotices(notCovered model.NotCovered) []model.Notice {
	var notices []model.Notice
	for _, mount := range notCovered.Mounts {
		if mount.Reason != model.SkipReasonExcluded {
			continue
		}
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
	for _, layer := range notCovered.WritableLayers {
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeLayerNotRestored,
			Message: fmt.Sprintf("%s's writable layer was not in the savepoint: it started fresh from its image",
				formatStatusReplica(layer.Service, layer.Replica)),
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
				facts.deployments.latest.Savepoint),
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
	deployment := facts.deployments.latest
	if deployment == nil || replica.InDeployment {
		return model.Notice{}, false
	}
	name := formatStatusReplica(service, replica.Number)
	if !replica.InSavepoint {
		return model.Notice{
			Code: model.NoticeCodeReplicaAdded,
			Message: fmt.Sprintf("%s is not in savepoint %s: added after the rollback", name,
				deployment.Savepoint),
		}, true
	}
	if !isRecreated(replica) {
		return model.Notice{}, false
	}
	var message strings.Builder
	fmt.Fprintf(&message, "%s was recreated outside arcave%s", name, formatStatusTime(replica.CreatedAt, facts.checkedAt))
	if replica.Replaces != "" {
		fmt.Fprintf(&message, " (compose replaced %s)", replica.Replaces)
	}
	if restoredLayer(facts.view.savepoint, service, replica.Number) {
		message.WriteString(": the writable layer the rollback restored is gone")
	}
	message.WriteString("; volumes are untouched")
	return model.Notice{
		Code:    model.NoticeCodeReplicaRecreated,
		Message: message.String(),
	}, true
}

// isRecreated reports whether the replica is one the deployed savepoint recorded that something
// other than the deployment built since.
func isRecreated(replica model.ReplicaStatus) bool {
	return replica.InSavepoint && !replica.InDeployment && replica.State != model.ReplicaStateMissing
}

// restoredLayer reports whether the deployed savepoint's record holds a writable layer of the
// replica numbered number; false with no record.
func restoredLayer(savepoint *model.Savepoint, service string, number int) bool {
	return savepoint != nil && slices.ContainsFunc(savepoint.WritableLayers, func(layer model.WritableLayer) bool {
		return layer.Service == service && layer.Replica == number
	})
}

// formatStatusReplica names a replica as compose numbers it: service-n.
func formatStatusReplica(service string, number int) string {
	return service + "-" + strconv.Itoa(number)
}

// formatStatusTime is " <age> ago" at checkedAt; empty for no time. An age reads the same in every
// zone the result is shown in.
func formatStatusTime(t *time.Time, checkedAt time.Time) string {
	if t == nil {
		return ""
	}
	return " " + model.FormatAge(checkedAt.Sub(*t)) + " ago"
}
