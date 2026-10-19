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
// with. Being a UUID it renders only as a canonical one; Validate refuses the zero value.
type SessionID uuid.UUID

// DefaultSessionName is DefaultSession's name, reserved for it.
const DefaultSessionName = "baseline"

// DefaultSession is the session a command works in unless it names another. It
// has no record and always exists implicitly. No UUIDv7 can collide with it.
var DefaultSession = SessionID(uuid.MustParse("00000000-0000-0000-0000-000000000001"))

// DefaultSessionRecord is DefaultSession as a record: nothing on disk backs it.
var DefaultSessionRecord = Session{
	ID:   DefaultSession,
	Name: DefaultSessionName,
}

// NewSessionID returns a fresh UUIDv7.
func NewSessionID() SessionID {
	return SessionID(uuid.Must(uuid.NewV7()))
}

// ParseSessionID accepts a UUID in its canonical form only; the round trip refuses
// braces, urn:uuid: and upper case, so a store root carries one spelling.
func ParseSessionID(s string) (SessionID, error) {
	parsed, err := uuid.Parse(s)
	if err != nil || parsed.String() != s {
		return SessionID{}, fmt.Errorf("%w %q: must be a canonical UUID", ErrInvalidSessionID, s)
	}
	id := SessionID(parsed)
	if err := id.Validate(); err != nil {
		return SessionID{}, err
	}
	return id, nil
}

// IsNil reports whether the id is the nil UUID, which names no session.
func (m SessionID) IsNil() bool { return uuid.UUID(m) == uuid.Nil }

// Validate refuses the nil UUID.
func (m SessionID) Validate() error {
	if m.IsNil() {
		return fmt.Errorf("%w: must not be the nil UUID", ErrInvalidSessionID)
	}
	return nil
}

// String renders the id as the path segment it becomes.
func (m SessionID) String() string { return uuid.UUID(m).String() }

// MarshalText renders the id in its canonical form.
func (m SessionID) MarshalText() ([]byte, error) { return uuid.UUID(m).MarshalText() }

// UnmarshalText reads a canonical id, as ParseSessionID does.
func (m *SessionID) UnmarshalText(data []byte) error {
	id, err := ParseSessionID(string(data))
	if err != nil {
		return err
	}
	*m = id
	return nil
}
