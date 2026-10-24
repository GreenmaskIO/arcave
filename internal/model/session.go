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

// SessionStatus is where a session's tree stands; only a ready one can be used.
type SessionStatus string

const (
	// SessionStatusReady is a session whose record is written; DefaultSession is always ready.
	SessionStatusReady SessionStatus = "ready"
	// SessionStatusCloning is a tree without a record whose fork still keeps its heartbeat fresh.
	SessionStatusCloning SessionStatus = "cloning"
	// SessionStatusFailed is a tree without a record whose fork failed or stopped beating.
	SessionStatusFailed SessionStatus = "failed"
	// SessionStatusIncomplete is a tree without a record and without a running or failed fork: an
	// interrupted delete, or a tree arcave did not write.
	SessionStatusIncomplete SessionStatus = "incomplete"
	// SessionStatusUnknown is a tree without a record whose heartbeat cannot be read, or reports
	// another operation still in progress.
	SessionStatusUnknown SessionStatus = "unknown"
)

// SessionSummary is one row of a session listing: the record plus what the session holds.
// A tree without a record carries what its heartbeat says of it.
type SessionSummary struct {
	Session
	Status SessionStatus `json:"status"`
	// Current marks the session commands work in when they name none.
	Current bool `json:"current"`
	// Error is why a failed fork failed, as its heartbeat records it, or why an unknown tree's
	// heartbeat could not be read.
	Error string `json:"error,omitempty"`
	// SavepointCount counts the session's savepoints, safety snapshots excluded.
	SavepointCount  int                        `json:"savepoint_count"`
	ComposeProjects []DockerComposeProjectName `json:"compose_projects"`
	// SizeBytes is what the session's tree occupies in the store: what deleting it frees.
	SizeBytes int64 `json:"size_bytes"`
}

// ListSessionsRequest carries what ListSessions needs.
type ListSessionsRequest struct {
	// All adds the trees without a record: forks cloning, failed or left incomplete.
	All bool
	// Ref marks as current the session it names; empty marks the current session.
	Ref string
	// Sort orders the listing by age; zero is newest first.
	Sort SortOrder
}

// RemoveSessionRequest carries what RemoveSession needs.
type RemoveSessionRequest struct {
	// Session names the session by name or id; a tree without a record only by id.
	Session string
	// Force deletes a tree still cloning or of unknown status, the current session, or Pinned.
	Force bool
	// Pinned is the session an MCP server is pinned to; zero is none.
	Pinned SessionID
}

// CreateSessionRequest carries what CreateSession needs.
type CreateSessionRequest struct {
	Name string // optional; unique within the arcave project when set
	// From names the session to clone, by name or id; empty starts the session empty.
	From string
}

// Validate refuses a name that is all whitespace.
func (m CreateSessionRequest) Validate() error {
	if m.Name != "" && strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w %q: must not be blank", ErrInvalidSessionName, m.Name)
	}
	return nil
}

// RemoveSessionResponse reports a removed session.
type RemoveSessionResponse struct {
	Session SessionID `json:"session"`
	Removed bool      `json:"removed"`
	// FreedBytes is the session's size, measured just before it was removed.
	FreedBytes int64 `json:"freed_bytes"`
}

// ForkHeartbeatData is a fork heartbeat's data.
type ForkHeartbeatData struct {
	From SessionID `json:"from"`
}
