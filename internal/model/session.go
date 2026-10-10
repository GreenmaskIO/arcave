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

import "time"

// Session is a session's record — sessions/<id>/session.json. DefaultSession has
// none and always exists implicitly.
type Session struct {
	ID        SessionID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// From is the savepoint the session was forked from; nil for one started empty.
	From *SessionOrigin `json:"from,omitempty"`
}

// SessionOrigin names the savepoint, in another session, a session was forked from.
type SessionOrigin struct {
	Session   SessionID   `json:"session"`
	Savepoint SavepointID `json:"savepoint"`
}

// ComposeProjectSavepoints is one compose project's committed savepoints, an entry
// of a session-wide listing.
type ComposeProjectSavepoints struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	Savepoints     []Savepoint              `json:"savepoints"`
}
