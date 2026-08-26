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

// SnapshotStat is the result of snapshotting one volume. Payload is the
// backend's own: full-copy — bytes stored, cdc — stream position.
//
// It is also the savepoint's per-volume bookkeeping: a restore addresses each
// archive from the Volume here. Payload decodes back as a map[string]any, which
// costs nothing while no reader outside its own backend interprets one.
type SnapshotStat struct {
	Volume  Volume              `json:"volume"`
	Type    SnapshotBackendKind `json:"type"`
	Payload any                 `json:"payload,omitempty"`
}
