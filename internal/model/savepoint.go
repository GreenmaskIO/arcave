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
	"time"
)

// The label a rollback stamps its safety snapshot with — the only thing telling
// it from an ordinary savepoint, and named by the service, the backends and both
// adapters alike.
const (
	SavepointLabelSafetySnapshot      = "arcave-safety-snapshot"
	SavepointLabelSafetySnapshotValue = "true"
)

// Savepoint is one savepoint as arcave reads it: the attributes it was taken with, which provider
// took it, what that provider sums it up as, and the provider's own body, which arcave never
// decodes. Its json tags are the wire contract: single-savepoint payloads marshal it directly.
type Savepoint struct {
	ID          SavepointID       `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels,omitempty"`
	// TakenBeforeRollbackTo is the savepoint the rollback this safety snapshot was taken before went
	// to, by the name it carried then; nil on any other savepoint. The label, not this field, is what
	// makes a record a safety snapshot.
	TakenBeforeRollbackTo *SavepointRef `json:"taken_before_rollback_to,omitempty"`
	CreatedAt             time.Time     `json:"created_at"`
	// Provider names the provider that took the savepoint and alone reads Detail.
	Provider string `json:"provider"`
	// Summary is what the provider sums Detail up as, derived on every read; a record on disk
	// carries none.
	Summary SavepointSummary `json:"summary,omitzero"`
	// Detail is the provider's body of the savepoint.
	Detail json.RawMessage `json:"detail,omitempty"`
}

// SavepointRef names a savepoint by id and by the name it carried then: the savepoint may since
// have been removed.
type SavepointRef struct {
	ID   SavepointID `json:"id"`
	Name string      `json:"name"`
}

// String names the savepoint by its name, by its id when it has none.
func (m SavepointRef) String() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID.String()
}

// IsSafetySnapshot reports whether this record is a rollback's undo rather than
// a savepoint a user asked for.
func (m Savepoint) IsSafetySnapshot() bool {
	return m.Labels[SavepointLabelSafetySnapshot] == SavepointLabelSafetySnapshotValue
}
