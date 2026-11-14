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

package compose

import (
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps the provider opens beside a capture's own: <namespace>.<action> for an
// action, a bare noun for an object.
const (
	kindSavepointHold    progress.Kind = "savepoint.hold"
	kindSavepointRelease progress.Kind = "savepoint.release"

	kindProjectIntrospect progress.Kind = "project.introspect"
	kindComposeDown       progress.Kind = "compose.down"
	kindComposeCreate     progress.Kind = "compose.create"
	kindComposeStart      progress.Kind = "compose.start"

	kindVolumeRestore  progress.Kind = "volume.restore"
	kindVolume         progress.Kind = "volume"
	kindLayerRestore   progress.Kind = "layer.restore"
	kindLayer          progress.Kind = "layer"
	kindReplicaRebuild progress.Kind = "replica.rebuild"
	kindReplica        progress.Kind = "replica"
)

// newRestoreSample is the sample a restored unit ends with: its size.
func newRestoreSample(size int64) progress.Sample {
	return progress.Sample{Bytes: &progress.Bytes{
		Done:  size,
		Total: size,
	}}
}
