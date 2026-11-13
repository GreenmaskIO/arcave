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

package core

import (
	"context"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps core opens: <namespace>.<action> for an action, a bare noun for an object.
// volume and layer are duplicated in capture; keep both in step.
const (
	kindSavepointCreate progress.Kind = "savepoint.create"
	kindSavepointRemove progress.Kind = "savepoint.remove"
	kindSavepointDrop   progress.Kind = "savepoint.drop"
	kindSavepoint       progress.Kind = "savepoint"

	kindRollback               progress.Kind = "rollback"
	kindRollbackPrepare        progress.Kind = "rollback.prepare"
	kindRollbackSafetySnapshot progress.Kind = "rollback.safety-snapshot"
	kindRollbackApply          progress.Kind = "rollback.apply"
	kindRollbackRevert         progress.Kind = "rollback.revert"
	kindRollbackCleanup        progress.Kind = "rollback.cleanup"

	kindSessionRemove progress.Kind = "session.remove"
	kindSessionLock   progress.Kind = "session.lock"
	kindSessionFork   progress.Kind = "session.fork"

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

// runStep runs fn under a workflow step of kind, ended with fn's error.
func runStep(ctx context.Context, kind progress.Kind, fn func(ctx context.Context) error) error {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kind})
	err := fn(ctx)
	step.End(err)
	return err
}

// runComposeActivity runs fn as one compose activity of kind, a line per object compose reports.
func runComposeActivity(ctx context.Context, kind progress.Kind, fn func(ctx context.Context) error) error {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kind}, progress.MeasureResources)
	err := fn(ctx)
	step.End(err)
	return err
}

// newRestoreSample is the sample a restored unit ends with: its size.
func newRestoreSample(size int64) progress.Sample {
	return progress.Sample{Bytes: &progress.Bytes{
		Done:  size,
		Total: size,
	}}
}
