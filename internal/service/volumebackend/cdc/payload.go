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

package cdc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// errNoSnapshotID reports a capture that named no snapshot: restic printed none,
// or the record carries none. Its *shape* is never judged — the id format is
// restic's contract, not arcave's — only its presence, which is arcave's own.
var errNoSnapshotID = errors.New("no restic snapshot id")

// Payload is what a cdc capture reports in its SnapshotStat, and the only way a
// restore can address its data: a restic snapshot id is content-derived, so
// unlike a fullcopy archive key it cannot be recomputed.
type Payload struct {
	SnapshotObjectID string `json:"restic_snapshot_id"`
}

// DecodePayload reads back what a capture reported. A record that has been
// through the store arrives as a map[string]any, so the JSON round trip is the
// normal path, not a fallback.
func DecodePayload(stored any) (Payload, error) {
	if payload, ok := stored.(Payload); ok {
		return payload, nil
	}
	if stored == nil {
		return Payload{}, errors.New("no cdc payload: this capture reported none")
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return Payload{}, fmt.Errorf("re-encode cdc payload: %w", err)
	}
	var payload Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Payload{}, fmt.Errorf("decode cdc payload: %w", err)
	}
	return payload, nil
}
