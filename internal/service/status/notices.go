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

	"github.com/greenmaskio/arcave/internal/model"
)

// statusFacts is everything arcave's own notices are read off.
type statusFacts struct {
	// arcaveProject is the one the config names.
	arcaveProject model.ArcaveProjectName
	// ref is the request's --session, empty when none was given.
	ref        string
	marker     *model.InflightMarker
	deployment *model.SavepointDeployment
	view       storeView
	unreadable []model.StatusUnreadable
}

// newStatusNotices is arcave's notices and the provider's, one per thing that is wrong or worth
// knowing, the severe first.
func newStatusNotices(facts statusFacts, reported []model.Notice) []model.Notice {
	var notices []model.Notice
	if facts.marker != nil {
		notices = append(notices, newInflightNotice(*facts.marker))
	}
	for _, unreadable := range facts.unreadable {
		notices = append(notices, newUnreadableNotice(unreadable, facts.ref))
	}
	if facts.deployment != nil {
		notices = append(notices, newDeploymentNotices(facts, *facts.deployment)...)
	}
	notices = append(notices, reported...)
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

// newDeploymentNotices are what the deployment itself says: another arcave project or session than
// the one commands work in, a safety snapshot deployed, a savepoint the store no longer holds.
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
	if facts.view.record == model.DeploymentRecordMissing {
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeDeployedSavepointGone,
			Message: fmt.Sprintf("savepoint %s is no longer in session %s",
				deployment.Savepoint, deployment.Session),
		})
	}
	return notices
}
