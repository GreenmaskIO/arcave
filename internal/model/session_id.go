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

	"github.com/google/uuid"
)

// SessionID names a session — the lifetime everything under a store root dies
// with. It carries its own gate, so a value that reached a store was validated.
type SessionID string

// DefaultSession is the session segment every store is rooted under today. A
// session owns everything that dies with it, and there is exactly one, so
// nothing selects it yet — the segment is laid down now because introducing it
// later would move every existing key.
const DefaultSession SessionID = "global"

// Validate accepts DefaultSession or a UUID in its canonical form.
//
// SavepointID.Validate's twin one level up, and an allowlist for the same
// reason: the id is rendered into a store root, and a list of what a directory
// name must not be is never provably complete, while two known shapes are. The
// canonical spelling is required, since uuid.Parse also takes braces, a
// urn:uuid: prefix and upper case.
func (m SessionID) Validate() error {
	if m == DefaultSession {
		return nil
	}
	parsed, err := uuid.Parse(string(m))
	if err != nil || parsed.String() != string(m) {
		return fmt.Errorf("invalid session id %q: must be %q or a canonical UUID", string(m), DefaultSession)
	}
	return nil
}

// String renders the id as the path segment it becomes.
func (m SessionID) String() string { return string(m) }
