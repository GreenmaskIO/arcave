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

package garbage

import (
	"context"
	"errors"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// GarbageEraser carries out one GarbageAction.
type GarbageEraser interface {
	Action() model.GarbageAction
}

// DaemonEraser carries out one GarbageAction on the daemon's garbage.
type DaemonEraser interface {
	GarbageEraser
	// Erase carries out ops, every one of its action, through DeleteOps where it can.
	Erase(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion
}

// ComposeProjectEraser carries out one GarbageAction on a compose project's garbage.
type ComposeProjectEraser interface {
	GarbageEraser
	// Erase carries out ops, every one of its action, in store, through DeleteOps where it can.
	Erase(ctx context.Context, store storages.Storager, ops []model.GarbageOp) model.GarbageDeletion
}

// eraserSet finds the eraser of an action at each level; an action none carries out fails its
// ops (errNoEraser).
type eraserSet struct {
	daemon         map[model.GarbageAction]DaemonEraser
	composeProject map[model.GarbageAction]ComposeProjectEraser
}

// newEraserSet is the erasers of each level by their action.
func newEraserSet(daemon []DaemonEraser, composeProject []ComposeProjectEraser) eraserSet {
	return eraserSet{
		daemon:         newEraserMap(daemon),
		composeProject: newEraserMap(composeProject),
	}
}

// newEraserMap is erasers by their action.
func newEraserMap[E GarbageEraser](erasers []E) map[model.GarbageAction]E {
	byAction := make(map[model.GarbageAction]E, len(erasers))
	for _, eraser := range erasers {
		byAction[eraser.Action()] = eraser
	}
	return byAction
}

// findEraser is the eraser carrying out ops, every one of one action; none when ops is empty.
func findEraser[E GarbageEraser](erasers map[model.GarbageAction]E, ops []model.GarbageOp) (E, bool) {
	var zero E
	if len(ops) == 0 {
		return zero, false
	}
	for _, op := range ops[1:] {
		if op.Action != ops[0].Action {
			return zero, false
		}
	}
	eraser, ok := erasers[ops[0].Action]
	return eraser, ok
}

// DeleteOps runs del on each op in order until ctx is done; a target already gone counts as
// deleted, freeing nothing, and a failure ctx's end came with is interrupted.
func DeleteOps(
	ctx context.Context, ops []model.GarbageOp, del func(ctx context.Context, op model.GarbageOp) error,
) model.GarbageDeletion {
	var (
		deleted []model.GarbageDeleted
		failed  []model.GarbageDeleteFailure
		notRun  int
	)
	ctx, batch := BeginDeletion(ctx, len(ops))
	for i, op := range ops {
		if ctx.Err() != nil {
			notRun = len(ops) - i
			break
		}
		opCtx, step := BeginTarget(ctx, op.Target)
		switch err := del(opCtx, op); {
		case errors.Is(err, storages.ErrFileNotFound), errors.Is(err, model.ErrImageRefNotFound):
			step.Done(progress.Sample{})
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  new(int64),
			})
		case err != nil:
			step.End(err)
			zerolog.Ctx(ctx).Error().Err(err).Str("target", op.Target).Msg("delete garbage")
			failed = append(failed, model.GarbageDeleteFailure{
				Target:      op.Target,
				Error:       err.Error(),
				Interrupted: ctx.Err() != nil,
			})
		default:
			step.Done(NewFreedSample(op.Bytes))
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  op.Bytes,
			})
		}
	}
	deletion := model.GarbageDeletion{
		Deleted: deleted,
		Failed:  failed,
		NotRun:  notRun,
	}
	EndDeletion(ctx, batch, deletion)
	return deletion
}

// BeginDeletion opens the batch an eraser's n targets report under; EndDeletion ends it.
func BeginDeletion(ctx context.Context, n int) (context.Context, *progress.StepReporter) {
	return progress.Batch(ctx, progress.Step{Kind: kindGCDelete}, n)
}

// BeginTarget opens the step of one target of a deletion, measuring the bytes it frees.
func BeginTarget(ctx context.Context, target string) (context.Context, *progress.StepReporter) {
	return progress.Run(ctx, progress.Step{
		Kind: kindGarbage,
		ID:   target,
	}, progress.MeasureBytes)
}

// EndDeletion ends the batch BeginDeletion opened on ctx with what deletion did.
func EndDeletion(ctx context.Context, batch *progress.StepReporter, deletion model.GarbageDeletion) {
	batch.End(newDeletionError(ctx, &deletion))
}

// NewFreedSample is what deleting a target freed: its size, when known.
func NewFreedSample(bytes *int64) progress.Sample {
	if bytes == nil {
		return progress.Sample{}
	}
	return progress.Sample{Bytes: &progress.Bytes{
		Done:  *bytes,
		Total: *bytes,
	}}
}
