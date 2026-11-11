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
	"cmp"
	"fmt"
	"strings"
)

// CaptureDrift is what a savepoint captured otherwise than the latest savepoint before it: the
// units whose capture stamp both specs carry and whose decision differs.
type CaptureDrift struct {
	// Since is the savepoint compared with, as it was named when this one was taken.
	Since   SavepointRef    `json:"since"`
	Changes []CaptureChange `json:"changes"`
}

// CaptureChange is one unit captured otherwise: its service, its stamp's key without
// CaptureStampLabelPrefix (mount./var/lib/postgresql/data, writable-layer, image), and the
// decision before and now.
type CaptureChange struct {
	Service string               `json:"service"`
	Stamp   string               `json:"stamp"`
	Was     CaptureStampDecision `json:"was"`
	Now     CaptureStampDecision `json:"now"`
}

// CaptureStampDecision is what a capture stamp decides of its unit: whether it is captured, and by
// which backend — of a CaptureDecision, what two savepoints are compared by.
type CaptureStampDecision struct {
	Capture bool `json:"capture"`
	// Backend is the token the unit is captured by; empty when left out, or when the unit has none.
	Backend string `json:"backend,omitempty"`
}

// NewCaptureStampDecision is the decision of a stamp.
func NewCaptureStampDecision(stamp CaptureStamp) CaptureStampDecision {
	return CaptureStampDecision{Capture: stamp.Capture, Backend: stamp.Backend}
}

// String is "left out", else the backend, else "kept".
func (m CaptureStampDecision) String() string {
	if !m.Capture {
		return "left out"
	}
	return cmp.Or(m.Backend, "kept")
}

// Notices spells out the drift, one notice per service in the order of the changes, none severe:
// a change of backend puts nothing at risk. Nil on a nil drift.
func (m *CaptureDrift) Notices() []Notice {
	if m == nil {
		return nil
	}
	var notices []Notice
	for i := 0; i < len(m.Changes); {
		service := m.Changes[i].Service
		var units []string
		for ; i < len(m.Changes) && m.Changes[i].Service == service; i++ {
			change := m.Changes[i]
			units = append(units, fmt.Sprintf("%s %s → %s",
				strings.Replace(change.Stamp, ".", " ", 1), change.Was, change.Now))
		}
		notices = append(notices, Notice{
			Message: fmt.Sprintf("%s captured otherwise than in savepoint %s: %s",
				service, m.Since, strings.Join(units, ", ")),
		})
	}
	return notices
}
