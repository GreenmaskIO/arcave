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
	"slices"

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

// eraserSet finds the eraser of an action at each level; an action none carries out is only
// reported.
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

// deleteGarbage deletes what found's collectors found, the daemon's reports first, report by
// report, until a Verify refuses or ctx is done; when that left an op not run, it returns why.
func (e eraserSet) deleteGarbage(
	ctx context.Context, grantedLock model.GrantedSessionLock, found garbageFound,
) (garbageFound, error) {
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var (
		composeProjects = make([]composeProjectGarbage, 0, len(found.composeProjects))
		daemon, stopped = deleteFindings(ctx, stop, grantedLock, found.daemon, e.daemon,
			func(ctx context.Context, eraser DaemonEraser, ops []model.GarbageOp) model.GarbageDeletion {
				return eraser.Erase(ctx, ops)
			})
	)
	for _, garbage := range found.composeProjects {
		projectCtx, node := progress.Begin(ctx, progress.Step{
			Kind: kindComposeProject,
			Name: garbage.scope.ComposeProject.String(),
		})
		reports, reportsStopped := deleteFindings(projectCtx, stop, grantedLock, garbage.reports, e.composeProject,
			func(ctx context.Context, eraser ComposeProjectEraser, ops []model.GarbageOp) model.GarbageDeletion {
				return eraser.Erase(ctx, garbage.store, ops)
			},
		)
		node.End(newReportsError(projectCtx, reports))
		stopped = stopped || reportsStopped
		composeProjects = append(composeProjects, composeProjectGarbage{
			scope:   garbage.scope,
			store:   garbage.store,
			reports: reports,
		})
	}
	deleted := garbageFound{
		daemon:          daemon,
		composeProjects: composeProjects,
	}
	if !stopped {
		// An interrupt past the last deletion cut nothing short.
		return deleted, nil
	}
	return deleted, context.Cause(ctx)
}

// deleteFindings deletes what each report found, in order, through erase, stopping the run once a
// Verify refuses; stopped is whether the stop cut any op short, left not run or failed interrupted.
func deleteFindings[E GarbageEraser](
	ctx context.Context, stop context.CancelCauseFunc, grantedLock model.GrantedSessionLock,
	reports []model.GarbageReport, erasers map[model.GarbageAction]E,
	erase func(ctx context.Context, eraser E, ops []model.GarbageOp) model.GarbageDeletion,
) (deleted []model.GarbageReport, stopped bool) {
	deleted = make([]model.GarbageReport, 0, len(reports))
	for _, report := range reports {
		deletion, err := deleteReportFindings(ctx, grantedLock, report, erasers, erase)
		if err != nil {
			stop(err)
		}
		stopped = stopped || deletion != nil && deletion.IsInterrupted()
		deleted = append(deleted, withDeletion(report, deletion))
	}
	return deleted, stopped
}

// deleteReportFindings is what deleting the ops report found did: nil when no eraser carries out
// their action or the report was aborted or failed, every op not run once ctx is done. A Verify
// that refuses is returned.
func deleteReportFindings[E GarbageEraser](
	ctx context.Context, grantedLock model.GrantedSessionLock, report model.GarbageReport,
	erasers map[model.GarbageAction]E,
	erase func(ctx context.Context, eraser E, ops []model.GarbageOp) model.GarbageDeletion,
) (*model.GarbageDeletion, error) {
	if report.Detail == nil || report.Summary.Aborted != "" {
		return nil, nil
	}
	ops := report.Detail.Findings().Ops
	if len(ops) == 0 {
		return &model.GarbageDeletion{}, nil
	}
	eraser, ok := findEraser(erasers, ops)
	switch {
	case !ok:
		return nil, nil
	case ctx.Err() != nil:
		return &model.GarbageDeletion{NotRun: len(ops)}, nil
	}
	if err := grantedLock.Verify(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("data_kind", string(report.DataKind)).
			Msg("session lock no longer vouches for the gc: nothing more is deleted")
		return &model.GarbageDeletion{NotRun: len(ops)}, fmt.Errorf("verify session lock: %w", err)
	}
	reportCtx, node := progress.Begin(ctx, progress.Step{
		Kind: kindCollector,
		ID:   string(report.Collector),
	})
	deletion := erase(reportCtx, eraser, ops)
	node.End(newDeletionError(reportCtx, &deletion))
	zerolog.Ctx(ctx).Info().Str("data_kind", string(report.DataKind)).
		Str("compose_project", report.ComposeProject.String()).
		Int("deleted", len(deletion.Deleted)).Int("failed", len(deletion.Failed)).Int("not_run", deletion.NotRun).
		Msg("garbage deleted")
	return &deletion, nil
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
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindGCDelete}, len(ops))
	for i, op := range ops {
		if ctx.Err() != nil {
			notRun = len(ops) - i
			break
		}
		opCtx, node := progress.Run(ctx, progress.Step{Kind: kindGarbage, ID: op.Target}, progress.MeasureBytes)
		switch err := del(opCtx, op); {
		case errors.Is(err, storages.ErrFileNotFound), errors.Is(err, model.ErrImageRefNotFound):
			node.Done(progress.Sample{})
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  new(int64),
			})
		case err != nil:
			node.End(err)
			zerolog.Ctx(ctx).Error().Err(err).Str("target", op.Target).Msg("delete garbage")
			failed = append(failed, model.GarbageDeleteFailure{
				Target:      op.Target,
				Error:       err.Error(),
				Interrupted: ctx.Err() != nil,
			})
		default:
			node.Done(newFreedSample(op.Bytes))
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  op.Bytes,
			})
		}
	}
	// A stop that came past the last op cut nothing short.
	cutShort := notRun > 0 || slices.ContainsFunc(failed, func(f model.GarbageDeleteFailure) bool { return f.Interrupted })
	switch {
	case cutShort:
		batch.End(context.Cause(ctx))
	case len(failed) > 0:
		batch.End(fmt.Errorf("%d deletion(s) failed", len(failed)))
	default:
		batch.End(nil)
	}
	return model.GarbageDeletion{
		Deleted: deleted,
		Failed:  failed,
		NotRun:  notRun,
	}
}

func withDeletion(report model.GarbageReport, deletion *model.GarbageDeletion) model.GarbageReport {
	report.Deletion = deletion
	return report
}

// newUndeletedWarnings names, once each, the data kinds found with garbage but no deletion yet.
func newUndeletedWarnings(reports []model.GarbageReport) []string {
	var (
		named    = map[model.GarbageDataKind]bool{}
		warnings []string
	)
	for _, report := range reports {
		undeleted := report.Deletion == nil && report.Summary.Aborted == "" && report.Summary.Found > 0
		if !undeleted || named[report.DataKind] {
			continue
		}
		named[report.DataKind] = true
		warnings = append(warnings, fmt.Sprintf("%s is reported, not deleted, yet", report.DataKind))
	}
	return warnings
}

// newFreedSample is what deleting an op freed: its size, when known.
func newFreedSample(bytes *int64) progress.Sample {
	if bytes == nil {
		return progress.Sample{}
	}
	return progress.Sample{Bytes: &progress.Bytes{
		Done:  *bytes,
		Total: *bytes,
	}}
}

// newReportsError is what a compose project's deletions, under a node opened on ctx, end with: the
// first report's that failed.
func newReportsError(ctx context.Context, reports []model.GarbageReport) error {
	for _, report := range reports {
		if err := newDeletionError(ctx, report.Deletion); err != nil {
			return err
		}
	}
	return nil
}
