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

// SavepointID addresses one capture — a savepoint's or a safety snapshot's — and
// doubles as the directory it lives in. It carries its own gate, so a value that
// reached a storage key was validated.
type SavepointID string

// NewSavepointID returns a fresh UUIDv7: its canonical form sorts by creation
// time, so a store listing directories by name lists savepoints oldest first.
func NewSavepointID() SavepointID {
	return SavepointID(uuid.Must(uuid.NewV7()).String())
}

// Validate accepts a canonical UUID only, which cannot name a parent, an absolute path
// or a sibling such as shared/; the round trip refuses braces, urn:uuid: and upper case.
func (m SavepointID) Validate() error {
	if m == "" {
		return fmt.Errorf("%w: must not be empty", ErrInvalidSavepointID)
	}
	parsed, err := uuid.Parse(string(m))
	if err != nil || parsed.String() != string(m) {
		return fmt.Errorf("%w %q: must be a canonical UUID", ErrInvalidSavepointID, string(m))
	}
	return nil
}

// String renders the id as the path segment it becomes.
func (m SavepointID) String() string { return string(m) }

// SavepointIDs converts adapter input — CLI args, a JSON array — into ids. It
// validates nothing: the layer that renders one into a storage key gates it.
func SavepointIDs(ids []string) []SavepointID {
	out := make([]SavepointID, len(ids))
	for i, id := range ids {
		out[i] = SavepointID(id)
	}
	return out
}
