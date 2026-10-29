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
	"fmt"
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the nodes core opens: <namespace>.<action> for an action, a bare noun for an object.
const (
	kindSavepointCreate  progress.Kind = "savepoint.create"
	kindSavepointRemove  progress.Kind = "savepoint.remove"
	kindSavepointCapture progress.Kind = "savepoint.capture"
	kindSavepointDrop    progress.Kind = "savepoint.drop"
	kindSavepoint        progress.Kind = "savepoint"

	kindRollback               progress.Kind = "rollback"
	kindRollbackPrepare        progress.Kind = "rollback.prepare"
	kindRollbackSafetySnapshot progress.Kind = "rollback.safety-snapshot"
	kindRollbackApply          progress.Kind = "rollback.apply"
	kindRollbackRevert         progress.Kind = "rollback.revert"
	kindRollbackReap           progress.Kind = "rollback.reap"

	kindSessionRemove progress.Kind = "session.remove"
	kindSessionLock   progress.Kind = "session.lock"
	kindSessionFork   progress.Kind = "session.fork"

	kindProjectIntrospect progress.Kind = "project.introspect"
	kindComposeStop       progress.Kind = "compose.stop"
	kindComposeDown       progress.Kind = "compose.down"
	kindComposeCreate     progress.Kind = "compose.create"
	kindComposeStart      progress.Kind = "compose.start"

	kindVolume         progress.Kind = "volume"
	kindImageCapture   progress.Kind = "image.capture"
	kindImage          progress.Kind = "image"
	kindLayerCapture   progress.Kind = "layer.capture"
	kindLayerRestore   progress.Kind = "layer.restore"
	kindLayer          progress.Kind = "layer"
	kindReplicaRebuild progress.Kind = "replica.rebuild"
	kindReplica        progress.Kind = "replica"
)

// newVolumeOpKind is the batch kind of one volume operation: volume.snapshot, volume.restore,
// volume.remove.
func newVolumeOpKind(opName string) progress.Kind {
	return progress.Kind("volume." + opName)
}

// runStep runs fn under a workflow node of kind, ended with fn's error.
func runStep(ctx context.Context, kind progress.Kind, fn func(ctx context.Context) error) error {
	ctx, node := progress.Begin(ctx, progress.Step{Kind: kind})
	err := fn(ctx)
	node.End(err)
	return err
}

// runActivity runs fn as one activity of kind, ended with fn's error.
func runActivity(ctx context.Context, kind progress.Kind, fn func(ctx context.Context) error) error {
	ctx, node := progress.Run(ctx, progress.Step{Kind: kind})
	err := fn(ctx)
	node.End(err)
	return err
}

// runComposeActivity runs fn as one compose activity of kind, a line per object compose reports.
func runComposeActivity(ctx context.Context, kind progress.Kind, fn func(ctx context.Context) error) error {
	ctx, node := progress.Run(ctx, progress.Step{Kind: kind}, progress.MeasureResources)
	err := fn(ctx)
	node.End(err)
	return err
}

// newCaptureSample is the sample a captured unit ends with: its size, what it stored, and what of
// it was found already stored.
func newCaptureSample(size, stored, reused int64) progress.Sample {
	return progress.Sample{Bytes: &progress.Bytes{
		Done:   size,
		Total:  size,
		Stored: stored,
		Reused: reused,
	}}
}

// newRestoreSample is the sample a restored unit ends with: its size.
func newRestoreSample(size int64) progress.Sample {
	return progress.Sample{Bytes: &progress.Bytes{
		Done:  size,
		Total: size,
	}}
}

// warn reports msg on node, marked InResult when inResult is set.
func warn(node *progress.Node, msg string, inResult bool) {
	if inResult {
		node.WarnInResult(msg)
		return
	}
	node.Warn(msg)
}

// isInResult reports whether a capture under labels is its run's result, i.e. no safety snapshot.
func isInResult(labels map[string]string) bool {
	return !model.Savepoint{Labels: labels}.IsSafetySnapshot()
}

// newNotCoveredWarning sums up what a capture leaves out, "" for nothing.
func newNotCoveredWarning(notCovered *model.NotCovered) string {
	if notCovered == nil {
		return ""
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(len(notCovered.Mounts), "mount(s)")
	add(len(notCovered.ServicesWithoutVolumes), "service(s) without volumes")
	add(len(notCovered.KilledContainers), "killed container(s)")
	add(len(notCovered.ReplicaGaps), "replica gap(s)")
	add(len(notCovered.UnresolvedImages), "unresolved image(s)")
	add(len(notCovered.WritableLayers), "writable layer(s)")
	add(len(notCovered.Images), "image(s)")
	if len(parts) == 0 {
		return ""
	}
	return "not covered: " + strings.Join(parts, ", ")
}
