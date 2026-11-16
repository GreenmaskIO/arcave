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

// DownRequest carries what Down needs.
type DownRequest struct {
	// Force takes the environment down whoever deployed it, and clears an inflight marker of this
	// compose project.
	Force bool
}

// DownResult is what a down took off the daemon, and the JSON 'arcave down' prints.
type DownResult struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	// Session is the session the down ran in.
	Session SessionRef `json:"session"`
	// Deployment is what the replicas' labels named; nil when none did.
	Deployment *SavepointDeployment `json:"deployment,omitempty"`
	// Forced says the owner was not checked.
	Forced bool `json:"forced"`
	// ClearedInflight is the inflight marker a forced down cleared; nil when it cleared none.
	ClearedInflight *InflightMarker    `json:"cleared_inflight,omitempty"`
	Discarded       DiscardedResources `json:"discarded"`
	// RemovedTags are the arcave/…:sp-<savepoint> tags of the compose project's restored layers.
	RemovedTags []ImageRef `json:"removed_tags"`
	Notices     []Notice   `json:"notices,omitempty"`
}
