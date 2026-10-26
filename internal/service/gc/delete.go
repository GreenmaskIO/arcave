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
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// DeleteOps runs del on each op in order until ctx is done; a target already gone counts as deleted.
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
		if err := del(ctx, op); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
			zerolog.Ctx(ctx).Error().Err(err).Str("target", op.Target).Msg("delete garbage")
			failed = append(failed, model.GarbageDeleteFailure{
				Target: op.Target,
				Error:  err.Error(),
			})
			continue
		}
		deleted = append(deleted, model.GarbageDeleted{
			Target: op.Target,
			Bytes:  op.Bytes,
		})
	}
	return model.GarbageDeletion{
		Deleted: deleted,
		Failed:  failed,
		NotRun:  notRun,
	}
}

// deleteGarbage deletes what found's compose-project collectors found, collector by collector, until
// a Verify refuses or ctx is done; it returns why it stopped, with every later op not run.
func (c *collectorSet) deleteGarbage(
	ctx context.Context, grantedLock model.GrantedSessionLock, found garbageFound,
) (garbageFound, error) {
	var (
		composeProjects = make([]composeProjectGarbage, 0, len(found.composeProjects))
		verifyErr       error
	)
	for _, garbage := range found.composeProjects {
		reports := make([]model.GarbageReport, 0, len(garbage.reports))
		for i, collector := range c.composeProject {
			var deletion *model.GarbageDeletion
			switch deleter, ops, ok := toDeleter(collector, garbage.reports[i]); {
			case !ok:
			case len(ops) == 0:
				deletion = &model.GarbageDeletion{Deleted: []model.GarbageDeleted{}}
			case verifyErr != nil, ctx.Err() != nil:
				deletion = newNotRunDeletion(ops)
			default:
				deletion, verifyErr = runDelete(ctx, deleter, garbage.scope, garbage.reports[i], grantedLock)
			}
			reports = append(reports, withDeletion(garbage.reports[i], deletion))
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
	return deleted, cmp.Or(verifyErr, context.Cause(ctx))
}

// toDeleter is collector as a deleter of report's ops; false for a collector that only reports,
// or a report that failed or was aborted.
func toDeleter(
	collector ComposeProjectCollector, report model.GarbageReport,
) (composeProjectDeleter, []model.GarbageOp, bool) {
	deleter, ok := collector.(composeProjectDeleter)
	if !ok || report.Detail == nil || report.Error != "" || report.Summary.Aborted != "" {
		return nil, nil, false
	}
	return deleter, report.Detail.Findings().Ops, true
}

// newNotRunDeletion is a deletion that started on none of ops.
func newNotRunDeletion(ops []model.GarbageOp) *model.GarbageDeletion {
	return &model.GarbageDeletion{
		Deleted: []model.GarbageDeleted{},
		NotRun:  len(ops),
	}
}

// runDelete deletes report's ops while the lock still vouches; a refused Verify runs none of them
// and is returned.
func runDelete(
	ctx context.Context, deleter composeProjectDeleter, scope ComposeProjectScope, report model.GarbageReport,
	grantedLock model.GrantedSessionLock,
) (*model.GarbageDeletion, error) {
	ops := report.Detail.Findings().Ops
	if err := grantedLock.Verify(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("data_kind", string(report.DataKind)).
			Msg("session lock no longer vouches for the gc: nothing more is deleted")
		return newNotRunDeletion(ops), fmt.Errorf("verify session lock: %w", err)
	}
	deletion, err := deleter.Delete(ctx, scope, report.Detail)
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("data_kind", string(report.DataKind)).
			Str("compose_project", scope.ComposeProject.String()).Msg("garbage deletion failed")
		return newFailedDeletion(ops, err), nil
	}
	zerolog.Ctx(ctx).Info().Str("data_kind", string(report.DataKind)).
		Str("compose_project", scope.ComposeProject.String()).
		Int("deleted", len(deletion.Deleted)).Int("failed", len(deletion.Failed)).Int("not_run", deletion.NotRun).
		Msg("garbage deleted")
	return &deletion, nil
}

// newFailedDeletion is a deletion that failed on every one of ops, with err.
func newFailedDeletion(ops []model.GarbageOp, err error) *model.GarbageDeletion {
	failed := make([]model.GarbageDeleteFailure, 0, len(ops))
	for _, op := range ops {
		failed = append(failed, model.GarbageDeleteFailure{
			Target: op.Target,
			Error:  err.Error(),
		})
	}
	return &model.GarbageDeletion{
		Deleted: []model.GarbageDeleted{},
		Failed:  failed,
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
		undeleted := report.Deletion == nil && report.Error == "" && report.Summary.Aborted == "" && report.Summary.Found > 0
		if !undeleted || named[report.DataKind] {
			continue
		}
		named[report.DataKind] = true
		warnings = append(warnings, fmt.Sprintf("%s is reported, not deleted, yet", report.DataKind))
	}
	return warnings
}
