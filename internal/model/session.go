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

// SessionRef names a session by id and by the name it carried.
type SessionRef struct {
	ID   SessionID `json:"id"`
	Name string    `json:"name"`
}

// String names the session by its name, by its id when it has none.
func (m SessionRef) String() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID.String()
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
	SavepointCount int `json:"savepoint_count"`
	// SafetySnapshotCount counts the session's safety snapshots.
	SafetySnapshotCount int `json:"safety_snapshot_count"`
	// ComposeProjects are the compose projects holding a savepoint or a safety snapshot, by name.
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

// FindSessionByRef finds the session ref names among sessions: by name first, then by id.
func FindSessionByRef(sessions []Session, ref string) (Session, error) {
	if session, found, err := FindSessionByName(sessions, ref); err != nil || found {
		return session, err
	}
	if session, found := FindSessionByID(sessions, ref); found {
		return session, nil
	}
	return Session{}, fmt.Errorf("%w: %q", ErrSessionNotFound, ref)
}

// FindSessionByName finds the one session carrying name; several carrying it is an error.
func FindSessionByName(sessions []Session, name string) (Session, bool, error) {
	var named []Session
	for _, session := range sessions {
		if session.Name == name {
			named = append(named, session)
		}
	}
	switch len(named) {
	case 0:
		return Session{}, false, nil
	case 1:
		return named[0], true, nil
	default:
		ids := make([]SessionID, len(named))
		for i, session := range named {
			ids[i] = session.ID
		}
		return Session{}, false, &AmbiguousSessionNameError{Name: name, IDs: ids}
	}
}

// FindSessionByID finds the session whose id is ref.
func FindSessionByID(sessions []Session, ref string) (Session, bool) {
	for _, session := range sessions {
		if session.ID.String() == ref {
			return session, true
		}
	}
	return Session{}, false
}

// ShowSessionRequest carries what ShowSession needs.
type ShowSessionRequest struct {
	// Session names the session by name or id; empty is the current session, else the default.
	Session string
	// Ref marks as current the session it names; empty marks the current session.
	Ref string
	// IncludeSafetySnapshots shows the safety snapshots too.
	IncludeSafetySnapshots bool
}

// SessionDetail is one session and its committed savepoints, by compose project.
type SessionDetail struct {
	Session Session `json:"session"`
	// Current marks the session commands work in when they name none, as a session listing does.
	Current bool `json:"current"`
	// From is the session this one was forked from, by id and its name now; no name once removed.
	From *SessionRef `json:"from,omitempty"`
	// SavepointCount counts the session's savepoints, safety snapshots excluded, shown or not.
	SavepointCount int `json:"savepoint_count"`
	// SafetySnapshotCount counts the session's safety snapshots, shown or not.
	SafetySnapshotCount int `json:"safety_snapshot_count"`
	// ComposeProjects are the compose projects holding a savepoint shown, by name, newest first within.
	ComposeProjects []ComposeProjectSavepoints `json:"compose_projects"`
	// CheckedAt is when the session was read; ages are counted to it.
	CheckedAt time.Time `json:"checked_at"`
}
