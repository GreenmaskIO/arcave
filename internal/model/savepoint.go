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
	"slices"
	"strings"
	"time"
)

// The label a rollback stamps its safety snapshot with — the only thing telling
// it from an ordinary savepoint, and named by the service, the backends and both
// adapters alike.
const (
	SavepointLabelSafetySnapshot      = "arcave-safety-snapshot"
	SavepointLabelSafetySnapshotValue = "true"
)

// SpecFormatComposeYAML is the format of a savepoint's spec artifact: a compose
// document, loadable by compose's own loader.
const SpecFormatComposeYAML = "compose-yaml"

// Savepoint is the metadata of one consistent snapshot of every registered
// stateful volume. The per-volume bookkeeping lives with the SavepointRecord
// that embeds this type. Its json tags are the wire contract: single-savepoint
// command payloads marshal this type directly.
type Savepoint struct {
	ID          SavepointID       `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Stats       []SnapshotStat    `json:"stats,omitempty"` // one per snapshotted volume
	// WritableLayers is one capture per container whose writable layer was committed;
	// empty when a capture takes none.
	WritableLayers []WritableLayer `json:"writable_layers,omitempty"`
	// Images is one entry per distinct image the compose file named, kept
	// whether or not a writable layer was captured over it.
	Images []Image `json:"images,omitempty"`
	// Spec names the compose spec committed beside this record. A savepoint without
	// one cannot be rolled back to: the compose project is rebuilt from the spec.
	Spec *SpecRef `json:"spec,omitempty"`
	// NotCovered is what this savepoint does not hold; nil when it holds everything.
	NotCovered *NotCovered `json:"not_covered,omitempty"`
	// CaptureDrift is every unit this savepoint captured otherwise than the latest savepoint
	// before it did; nil when none was, or when there was none to compare with.
	CaptureDrift *CaptureDrift `json:"capture_drift,omitempty"`
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

// SpecRef is the reference to a savepoint's compose spec artifact.
type SpecRef struct {
	Format string `json:"format"` // SpecFormatComposeYAML
}

// IsSafetySnapshot reports whether this record is a rollback's undo rather than
// a savepoint a user asked for.
func (m Savepoint) IsSafetySnapshot() bool {
	return m.Labels[SavepointLabelSafetySnapshot] == SavepointLabelSafetySnapshotValue
}

// Notices spells out what the savepoint leaves out and every image it names but does not store,
// the severe first.
func (m Savepoint) Notices() []Notice {
	var notices []Notice
	if m.NotCovered != nil {
		notices = m.NotCovered.Notices()
	}
	for _, image := range m.Images {
		if image.Stored {
			continue
		}
		notices = append(notices, Notice{
			Message: fmt.Sprintf("image %s is not stored, only its identity", newImageName(image)),
			Hint:    "a rollback needs it pullable to rebuild a container on it",
			Severe:  true,
		})
	}
	return SortSevereFirst(notices)
}

// SortSevereFirst orders notices severe first, keeping their order otherwise.
func SortSevereFirst(notices []Notice) []Notice {
	return slices.SortedStableFunc(slices.Values(notices), func(a, b Notice) int {
		return compareSevere(a.Severe, b.Severe)
	})
}

// newImageName is an image's first reference, else its short id.
func newImageName(image Image) string {
	if len(image.Refs) > 0 {
		return image.Refs[0]
	}
	return formatShortID(strings.TrimPrefix(image.ID.String(), "sha256:"))
}

// compareSevere orders a severe notice before one that only informs.
func compareSevere(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}
