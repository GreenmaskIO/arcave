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

package config

// LeftoverVolumesPolicy is what a rollback does with a volume that outlived its compose
// project's containers.
type LeftoverVolumesPolicy string

// The tokens compose.leftover_volumes accepts.
const (
	// LeftoverVolumesOverwrite restores over the volume and warns, naming it. The default.
	LeftoverVolumesOverwrite LeftoverVolumesPolicy = "overwrite"
	// LeftoverVolumesRefuse fails the rollback before anything is created.
	LeftoverVolumesRefuse LeftoverVolumesPolicy = "refuse"
)
