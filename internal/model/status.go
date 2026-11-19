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
	"encoding/json"
	"fmt"
	"time"
)

// StatusStateRollbackPending is the state of a workload an inflight marker names: a rollback is
// running, or was interrupted. Every other state is the provider's word.
const StatusStateRollbackPending = "rollback-pending"

// DeploymentRecordState says whether the store still holds the savepoint a rollback deployed.
type DeploymentRecordState string

const (
	DeploymentRecordFound   DeploymentRecordState = "found"
	DeploymentRecordMissing DeploymentRecordState = "missing"
	// DeploymentRecordUnreadable is a store that could not be read.
	DeploymentRecordUnreadable DeploymentRecordState = "unreadable"
)

// StatusUnreadableSource names what a status could not read.
type StatusUnreadableSource string

const (
	StatusUnreadableStore          StatusUnreadableSource = "store"
	StatusUnreadableSession        StatusUnreadableSource = "session"
	StatusUnreadableInflightMarker StatusUnreadableSource = "inflight-marker"
)

// StatusRequest carries what Status needs.
type StatusRequest struct {
	// Session names the session the user works in, as given; empty is the current session.
	Session string
}

// ComposeProjectStatus is what runs for a compose project, what built it, and where that stands
// against the store. Its json tags are the wire contract.
type ComposeProjectStatus struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	// CheckedAt is when the workload was read; every age is counted from it.
	CheckedAt time.Time `json:"checked_at"`
	// State is StatusStateRollbackPending under an inflight marker, else the provider's word.
	State string `json:"state"`
	// Session is the session arcave commands work in; nil when it could not be resolved.
	Session *SessionRef `json:"session"`
	// Deployment is the build the workload carries; nil for none.
	Deployment *DeploymentStatus `json:"deployment"`
	// RollbackInFlight is the inflight marker of this compose project: a rollback running, or
	// interrupted, which a status does not tell apart.
	RollbackInFlight *RollbackInFlight `json:"rollback_in_flight"`
	// Unreadable is what could not be read; any makes the status partial.
	Unreadable []StatusUnreadable `json:"unreadable"`
	// Notices are arcave's and the provider's, the severe first.
	Notices []Notice `json:"notices"`
	// Detail is the provider's body of the status.
	Detail WorkloadDetail `json:"detail"`
}

// WorkloadReport is what a provider says of a running workload; arcave prints it and reads none of it.
type WorkloadReport struct {
	// State sums the workload up in the provider's own word.
	State   string
	Notices []Notice
	Detail  WorkloadDetail
}

// WorkloadDetail is a provider's body of a report: its JSON for a machine, its table for a reader.
type WorkloadDetail interface {
	json.Marshaler
	// Table lists the workload's members as a text status prints them; no rows when nothing is there.
	Table() Table
}

// Table is rows of text under their column names.
type Table struct {
	Columns []string
	Rows    [][]string
}

// DeploymentStatus is the build a workload carries, as its stamp spells it, and whether the store
// still holds its savepoint.
type DeploymentStatus struct {
	DeployedAt time.Time `json:"deployed_at"`
	// ArcaveProject is the arcave project whose store holds Session.
	ArcaveProject ArcaveProjectName `json:"arcave_project"`
	// Session is the session the deployed savepoint was rolled back from.
	Session   SessionRef               `json:"session"`
	Savepoint *DeployedSavepointStatus `json:"savepoint"`
	// Record says whether that session still holds the savepoint; empty when the store was not
	// read for it.
	Record DeploymentRecordState `json:"record,omitempty"`
}

// DeployedSavepointStatus is the savepoint a deployment was built from: what its stamp says, and
// what its record adds while the store holds it.
type DeployedSavepointStatus struct {
	DeployedSavepoint
	// Description is the record's; empty when it was not found.
	Description string `json:"description,omitempty"`
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
