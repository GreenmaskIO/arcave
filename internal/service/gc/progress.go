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

package gc

import (
	"context"
	"errors"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the nodes a gc opens.
const (
	kindGC             progress.Kind = "gc"
	kindSessionLock    progress.Kind = "session.lock"
	kindGCFind         progress.Kind = "gc.find"
	kindGCErase        progress.Kind = "gc.erase"
	kindComposeProject progress.Kind = "compose.project"
	kindCollector      progress.Kind = "collector"
	kindGCDelete       progress.Kind = "gc.delete"
	kindGarbage        progress.Kind = "garbage"
)

// endCollectorNode ends a collector's node with what its report says: a failure on its error,
// what it held back or was refused as warnings the result repeats; one ctx's end cut short is cancelled.
func endCollectorNode(ctx context.Context, node *progress.Node, report model.GarbageReport) {
	summary := report.Summary
	if summary.Aborted != "" {
		node.WarnInResult("aborted: " + summary.Aborted)
	}
	if summary.Unjudged > 0 {
		node.WarnInResult(fmt.Sprintf("%d object(s) not judged", summary.Unjudged))
	}
	if summary.Skipped > 0 {
		node.WarnInResult(fmt.Sprintf("%d object(s) held back", summary.Skipped))
	}
	switch {
	case report.Error != "" && ctx.Err() != nil:
		node.Fail("", context.Cause(ctx))
		return
	case report.Error != "":
		node.Fail("", errors.New(report.Error))
		return
	}
	node.Update(fmt.Sprintf("%d found", summary.Found))
	node.Done(progress.Sample{})
}

// newDeletionError is what a deletion's node, opened on ctx, ends with: nil when every op ran and
// none failed, ctx's cause when its end cut the deletion short.
func newDeletionError(ctx context.Context, deletion *model.GarbageDeletion) error {
	switch {
	case deletion == nil || (len(deletion.Failed) == 0 && deletion.NotRun == 0):
		return nil
	case deletion.IsInterrupted() && ctx.Err() != nil:
		return context.Cause(ctx)
	}
	return fmt.Errorf("%d deletion(s) failed, %d not run", len(deletion.Failed), deletion.NotRun)
}
