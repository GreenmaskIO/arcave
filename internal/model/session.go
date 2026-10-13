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
	"strings"
	"time"
)

// Session is a session's record — sessions/<id>/session.json. DefaultSession has
// none and always exists implicitly.
type Session struct {
	ID        SessionID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	// From is the session this one was cloned from; nil for one started empty.
	From *SessionOrigin `json:"from,omitempty"`
}

// SessionOrigin names the session a session was cloned from.
type SessionOrigin struct {
	Session SessionID `json:"session"`
}

// ComposeProjectSavepoints is one compose project's committed savepoints, an entry
// of a session-wide listing.
type ComposeProjectSavepoints struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	Savepoints     []Savepoint              `json:"savepoints"`
}

// SessionSummary is one row of a session listing: the record plus what the session holds.
// DefaultSession's row carries only its id and name.
type SessionSummary struct {
	Session
	// SavepointCount counts the session's savepoints, safety snapshots excluded.
	SavepointCount  int                        `json:"savepoint_count"`
	ComposeProjects []DockerComposeProjectName `json:"compose_projects"`
	// SizeBytes is what the session's tree occupies in the store: what deleting it frees.
	SizeBytes int64 `json:"size_bytes"`
}

// CreateSessionRequest carries what CreateSession needs.
type CreateSessionRequest struct {
	Name string // optional; unique within the arcave project when set
	// From names the session to clone, by name or id; empty starts the session empty.
	From string
}

// Validate refuses a name that is all whitespace.
func (r CreateSessionRequest) Validate() error {
	if r.Name != "" && strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w %q: must not be blank", ErrInvalidSessionName, r.Name)
	}
	return nil
}

// DeleteSessionResponse reports a deleted session.
type DeleteSessionResponse struct {
	Session SessionID `json:"session"`
	Deleted bool      `json:"deleted"`
	// FreedBytes is the session's size, measured just before it was deleted.
	FreedBytes int64 `json:"freed_bytes"`
}

// HeartbeatOperationFork is session create --from in a heartbeat; its data is ForkHeartbeatData.
const HeartbeatOperationFork = "fork"

// ForkHeartbeatData is a fork heartbeat's data.
type ForkHeartbeatData struct {
	From SessionID `json:"from"`
}
