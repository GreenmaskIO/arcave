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

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// GarbageEraser carries out one GarbageAction on a compose project's garbage.
type GarbageEraser interface {
	Action() model.GarbageAction
	// Erase carries out ops, every one of its action, in scope, through DeleteOps where it can.
	Erase(ctx context.Context, scope ComposeProjectScope, ops []model.GarbageOp) model.GarbageDeletion
}

// eraserSet finds the eraser of an action; an action none carries out is only reported.
type eraserSet map[model.GarbageAction]GarbageEraser

// newEraserSet is erasers by their action.
func newEraserSet(erasers []GarbageEraser) eraserSet {
	set := make(eraserSet, len(erasers))
	for _, eraser := range erasers {
		set[eraser.Action()] = eraser
	}
	return set
}

// deleteGarbage deletes what found's compose-project collectors found, report by report, until a
// Verify refuses or ctx is done; when that left an op not run, it returns why.
func (e eraserSet) deleteGarbage(
	ctx context.Context, grantedLock model.GrantedSessionLock, found garbageFound,
) (garbageFound, error) {
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var (
		composeProjects = make([]composeProjectGarbage, 0, len(found.composeProjects))
		stopped         bool
	)
	for _, garbage := range found.composeProjects {
		reports := make([]model.GarbageReport, 0, len(garbage.reports))
		for _, report := range garbage.reports {
			deletion, err := e.deleteReport(ctx, grantedLock, garbage.scope, report)
			if err != nil {
				stop(err)
			}
			stopped = stopped || deletion != nil && deletion.NotRun > 0
			reports = append(reports, withDeletion(report, deletion))
		}
		composeProjects = append(composeProjects, composeProjectGarbage{
			scope:   garbage.scope,
			reports: reports,
		})
	}
	deleted := garbageFound{
		daemon:          found.daemon,
		composeProjects: composeProjects,
	}
	if !stopped {
		// An interrupt past the last deletion cut nothing short.
		return deleted, nil
	}
	return deleted, context.Cause(ctx)
}

// deleteReport is what deleting report's ops did: nil when no eraser carries out their action or
// the report was aborted or failed, every op not run once ctx is done. A Verify that refuses is
// returned.
func (e eraserSet) deleteReport(
	ctx context.Context, grantedLock model.GrantedSessionLock, scope ComposeProjectScope,
	report model.GarbageReport,
) (*model.GarbageDeletion, error) {
	if report.Detail == nil || report.Summary.Aborted != "" {
		return nil, nil
	}
	ops := report.Detail.Findings().Ops
	if len(ops) == 0 {
		return &model.GarbageDeletion{}, nil
	}
	eraser, ok := e.findEraser(ops)
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
	deletion := eraser.Erase(ctx, scope, ops)
	zerolog.Ctx(ctx).Info().Str("data_kind", string(report.DataKind)).
		Str("compose_project", scope.ComposeProject.String()).
		Int("deleted", len(deletion.Deleted)).Int("failed", len(deletion.Failed)).Int("not_run", deletion.NotRun).
		Msg("garbage deleted")
	return &deletion, nil
}

// findEraser is the eraser carrying out ops, every one of one action; none when ops is empty.
func (e eraserSet) findEraser(ops []model.GarbageOp) (GarbageEraser, bool) {
	if len(ops) == 0 {
		return nil, false
	}
	for _, op := range ops[1:] {
		if op.Action != ops[0].Action {
			return nil, false
		}
	}
	eraser, ok := e[ops[0].Action]
	return eraser, ok
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

// DeleteOps runs del on each op in order until ctx is done; a target already gone counts as
// deleted, freeing nothing.
func DeleteOps(
	ctx context.Context, ops []model.GarbageOp, del func(ctx context.Context, op model.GarbageOp) error,
) model.GarbageDeletion {
	var (
		deleted []model.GarbageDeleted
		failed  []model.GarbageDeleteFailure
		notRun  int
	)
	for i, op := range ops {
		if ctx.Err() != nil {
			notRun = len(ops) - i
			break
		}
		switch err := del(ctx, op); {
		case errors.Is(err, storages.ErrFileNotFound):
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  new(int64),
			})
		case err != nil:
			zerolog.Ctx(ctx).Error().Err(err).Str("target", op.Target).Msg("delete garbage")
			failed = append(failed, model.GarbageDeleteFailure{
				Target: op.Target,
				Error:  err.Error(),
			})
		default:
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  op.Bytes,
			})
		}
	}
	return model.GarbageDeletion{
		Deleted: deleted,
		Failed:  failed,
		NotRun:  notRun,
	}
}
