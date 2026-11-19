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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// statusFacts is everything a report's notices are read off.
type statusFacts struct {
	checkedAt   time.Time
	state       composemodel.ComposeProjectState
	deployments containerDeployments
	// savepoint is the deployed savepoint as its record holds it; nil for none read.
	savepoint *composemodel.Savepoint
	services  []composemodel.ServiceStatus
}

// newStatusNotices is one notice per thing that is wrong or worth knowing of the compose project,
// the severe first.
func newStatusNotices(facts statusFacts) []model.Notice {
	var notices []model.Notice
	for _, labels := range facts.deployments.unreadable {
		notices = append(notices, newUnreadableLabelsNotice(labels))
	}
	if facts.savepoint != nil {
		notices = append(notices, newNotRestoredNotices(facts.savepoint.Detail.NotCovered)...)
	}
	for _, service := range facts.services {
		for _, replica := range service.Replicas {
			notices = append(notices, newReplicaNotices(facts, service.Name, replica)...)
		}
	}
	if facts.state == composemodel.ComposeProjectStateStopped {
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeComposeProjectStopped,
			Message: "no container runs",
		})
	}
	// Non-nil, so none marshals as [], not null: slices.Concat of empty slices is nil.
	return append([]model.Notice{}, model.SortSevereFirst(notices)...)
}

// newUnreadableLabelsNotice names a container whose deployment labels do not parse: edited by hand,
// or written by another arcave.
func newUnreadableLabelsNotice(labels unreadableLabels) model.Notice {
	return model.Notice{
		Code: composemodel.NoticeCodeDeploymentLabelsUnreadable,
		Message: fmt.Sprintf("%s carries unreadable deployment labels: %s; read as deployed by none",
			formatStatusReplica(labels.service, labels.replica), labels.err),
	}
}

// newNotRestoredNotices say what the savepoint left out, so a rollback to it did not put back: a
// mount a label or the config excluded, and a writable layer.
func newNotRestoredNotices(notCovered *composemodel.NotCovered) []model.Notice {
	if notCovered == nil {
		return nil
	}
	var notices []model.Notice
	for _, mount := range notCovered.Mounts {
		if mount.Reason != composemodel.SkipReasonExcluded {
			continue
		}
		var message string
		switch {
		case mount.Volume != "":
			message = "volume " + mount.Volume + " was not in the savepoint: it holds what it held before the rollback"
		case mount.RawType == string(composemodel.VolumeTypeBind):
			message = fmt.Sprintf("%s %s was not in the savepoint: the host folder holds what it held before the rollback",
				mount.Service, mount.Target)
		default:
			message = fmt.Sprintf("%s %s was not in the savepoint: it came back empty",
				formatStatusReplica(mount.Service, mount.Replica), mount.Target)
		}
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeVolumeNotRestored,
			Message: message,
		})
	}
	for _, layer := range notCovered.WritableLayers {
		notices = append(notices, model.Notice{
			Code: composemodel.NoticeCodeLayerNotRestored,
			Message: fmt.Sprintf("%s's writable layer was not in the savepoint: it started fresh from its image",
				formatStatusReplica(layer.Service, layer.Replica)),
		})
	}
	return notices
}

// newReplicaNotices say what is wrong with one replica, or what happened to it since the rollback.
func newReplicaNotices(facts statusFacts, service string, replica composemodel.ReplicaStatus) []model.Notice {
	var (
		name    = formatStatusReplica(service, replica.Number)
		notices []model.Notice
	)
	switch replica.State {
	case composemodel.ReplicaStateMissing:
		return []model.Notice{{
			Code: composemodel.NoticeCodeReplicaMissing,
			Message: fmt.Sprintf("%s is missing: savepoint %s recorded it", name,
				facts.deployments.latest.Savepoint),
			Severe: true,
		}}
	case composemodel.ReplicaStateExited:
		// With every container stopped, compose-project-stopped says it once; a clean exit adds
		// nothing to that, a failing one is still a failure.
		if replica.ExitCode == 0 && facts.state == composemodel.ComposeProjectStateStopped {
			break
		}
		notices = append(notices, model.Notice{
			Code: composemodel.NoticeCodeReplicaExited,
			Message: fmt.Sprintf("%s exited with code %d%s%s", name, replica.ExitCode, formatExitCause(replica),
				formatStatusTime(replica.FinishedAt, facts.checkedAt)),
			Severe: replica.ExitCode != 0,
		})
	case composemodel.ReplicaStateDead:
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeReplicaDead,
			Message: name + " is dead",
			Severe:  true,
		})
	case composemodel.ReplicaStateRemoving:
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeReplicaRemoving,
			Message: name + " is being removed",
			Severe:  true,
		})
	case composemodel.ReplicaStateRestarting:
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeReplicaRestarting,
			Message: fmt.Sprintf("%s is restarting (%d restarts)", name, replica.RestartCount),
			Severe:  true,
		})
	case composemodel.ReplicaStateCreated:
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeReplicaCreated,
			Message: name + " was created but never started",
			Severe:  true,
		})
	case composemodel.ReplicaStatePaused:
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeReplicaPaused,
			Message: name + " is paused",
		})
	case composemodel.ReplicaStateRunning:
		if replica.RestartCount > 0 {
			notices = append(notices, model.Notice{
				Code:    composemodel.NoticeCodeReplicaRestarted,
				Message: fmt.Sprintf("%s restarted %d times since it was created", name, replica.RestartCount),
			})
		}
	}
	if replica.Health == composemodel.ReplicaHealthUnhealthy {
		notices = append(notices, model.Notice{
			Code:    composemodel.NoticeCodeReplicaUnhealthy,
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
func formatExitCause(replica composemodel.ReplicaStatus) string {
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
// or added after it; none for one whose labels do not parse, which another notice names.
func newDriftNotice(facts statusFacts, service string, replica composemodel.ReplicaStatus) (model.Notice, bool) {
	deployment := facts.deployments.latest
	if deployment == nil || replica.InDeployment || facts.deployments.isUnreadable(service, replica.Number) {
		return model.Notice{}, false
	}
	name := formatStatusReplica(service, replica.Number)
	if !replica.InSavepoint {
		return model.Notice{
			Code: composemodel.NoticeCodeReplicaAdded,
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
	if restoredLayer(facts.savepoint, service, replica.Number) {
		message.WriteString(": the writable layer the rollback restored is gone")
	}
	message.WriteString("; volumes are untouched")
	return model.Notice{
		Code:    composemodel.NoticeCodeReplicaRecreated,
		Message: message.String(),
	}, true
}

// isRecreated reports whether the replica is one the deployed savepoint recorded that something
// other than the deployment built since.
func isRecreated(replica composemodel.ReplicaStatus) bool {
	return replica.InSavepoint && !replica.InDeployment && replica.State != composemodel.ReplicaStateMissing
}

// restoredLayer reports whether the deployed savepoint's record holds a writable layer of the
// replica numbered number; false with no record.
func restoredLayer(savepoint *composemodel.Savepoint, service string, number int) bool {
	return savepoint != nil && slices.ContainsFunc(savepoint.Detail.WritableLayers, func(layer composemodel.WritableLayer) bool {
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
