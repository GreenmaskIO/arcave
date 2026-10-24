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

// HeartbeatOperation names the operation a heartbeat reports, and the shape of its data.
type HeartbeatOperation string

const (
	// HeartbeatOperationSavepoint is savepoint create, a safety snapshot included; it carries
	// no data.
	HeartbeatOperationSavepoint HeartbeatOperation = "savepoint"
	// HeartbeatOperationFork is session create --from; its data is ForkHeartbeatData.
	HeartbeatOperationFork HeartbeatOperation = "fork"
)
