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

import "slices"

// Notice is one thing a reader should know of a result: what a savepoint leaves out, what a build
// or a down warns of, what a status finds. Hint says what to change; Severe means the data is at
// risk and is listed first; Code, where defined, is stable for a machine to act on.
type Notice struct {
	// Code names the condition, stable for a machine to act on; empty where none is defined.
	Code    NoticeCode `json:"code,omitempty"`
	Message string     `json:"message"`
	Hint    string     `json:"hint,omitempty"`
	// Severe marks a notice that puts the data at risk; a reader lists it first.
	Severe bool `json:"severe,omitempty"`
}

// NoticeCode names the condition a notice reports.
type NoticeCode string

// The notices a status reports, one code per condition; a code is stable, its message is not.
const (
	NoticeCodeRollbackPending            NoticeCode = "rollback-pending"
	NoticeCodeStoreUnreadable            NoticeCode = "store-unreadable"
	NoticeCodeSessionUnresolved          NoticeCode = "session-unresolved"
	NoticeCodeInflightMarkerUnreadable   NoticeCode = "inflight-marker-unreadable"
	NoticeCodeDeploymentLabelsUnreadable NoticeCode = "deployment-labels-unreadable"
	NoticeCodeDeployedSavepointGone      NoticeCode = "deployed-savepoint-gone"
	NoticeCodeOtherArcaveProject         NoticeCode = "other-arcave-project"
	NoticeCodeOtherSession               NoticeCode = "other-session"
	NoticeCodeSafetySnapshotDeployed     NoticeCode = "safety-snapshot-deployed"
	NoticeCodeVolumeNotRestored          NoticeCode = "volume-not-restored"
	NoticeCodeLayerNotRestored           NoticeCode = "layer-not-restored"
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
	NoticeCodeComposeProjectStopped      NoticeCode = "compose-project-stopped"
)

// SortSevereFirst orders notices severe first, keeping their order otherwise.
func SortSevereFirst(notices []Notice) []Notice {
	return slices.SortedStableFunc(slices.Values(notices), func(a, b Notice) int {
		return compareSevere(a.Severe, b.Severe)
	})
}

// compareSevere orders a severe notice before one that only informs.
func compareSevere(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}
