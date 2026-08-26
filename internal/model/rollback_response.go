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

// RollbackResponse is the JSON printed by 'arcave rollback'. Restored names each
// unit by the same object id the storage layout is keyed by.
type RollbackResponse struct {
	RolledBackTo Savepoint      `json:"rolled_back_to"`
	Restored     []Volume       `json:"restored"`
	Images       []ImageRestore `json:"images,omitempty"`
}
