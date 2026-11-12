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

// UpRequest carries what Up needs: the savepoint to bring up and whether what the daemon still
// holds of the compose project may be dropped for it. No selector is the newest savepoint.
type UpRequest struct {
	ID     *SavepointID // exact id; wins over the two below
	Name   *string      // savepoint name; ambiguous names are an error, never a guess
	Latest *bool        // when true, the newest savepoint by CreatedAt

	WaitTimeout time.Duration // bounds the health check on start; <= 0 lets the compose runner apply its default

	// Force drops every container, volume and network of the compose project, with no backup,
	// before the savepoint is built.
	Force bool
}

// Selector is the rollback request the savepoint is resolved and built with: Latest when the
// request names neither an id nor a name, and no leftover-volumes policy, up judging those itself.
func (m UpRequest) Selector() RollbackRequest {
	latest := m.Latest
	if m.ID == nil && m.Name == nil {
		newest := true
		latest = &newest
	}
	return RollbackRequest{
		ID:          m.ID,
		Name:        m.Name,
		Latest:      latest,
		WaitTimeout: m.WaitTimeout,
	}
}

// Ref renders the selector back as the word the user typed, latest for none.
func (m UpRequest) Ref() string {
	return m.Selector().Ref()
}

// UpResult is what an up built, and the JSON 'arcave up' prints.
type UpResult struct {
	RolledBackTo   Savepoint                    `json:"rolled_back_to"`
	Restored       []Volume                     `json:"restored"`
	WritableLayers []WritableLayerRestoreResult `json:"writable_layers,omitempty"`
	// Discarded is what a forced up dropped first; absent when the daemon held nothing of the
	// compose project.
	Discarded *DiscardedResources `json:"discarded,omitempty"`
}

// DiscardedResources is what the daemon held of a compose project before it was dropped, as read
// then: compose does not say what it removed. An anonymous volume goes with its container and is
// not listed.
type DiscardedResources struct {
	Containers []string `json:"containers"`
	Volumes    []string `json:"volumes"`
	Networks   []string `json:"networks"`
}

// NewDiscardedResources is what dropping resources removes, every list non-nil.
func NewDiscardedResources(resources ComposeProjectResources) DiscardedResources {
	return DiscardedResources{
		Containers: nonNil(resources.Containers),
		Volumes:    nonNil(resources.Volumes),
		Networks:   nonNil(resources.Networks),
	}
}

// nonNil is names, or an empty list for none, so it marshals as [] rather than null.
func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}
