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
	"fmt"
	"slices"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// retired is one compose project's safety snapshots beyond the retention, and the savepoints they
// are removed through.
type retired struct {
	savepoints provider.Savepoints
	report     model.GarbageReport
}

// Report is what the retention found.
func (r retired) Report() model.GarbageReport {
	return r.report
}

// Delete removes every safety snapshot the report names, in order, until ctx is done; their blobs
// and restic snapshots go on the next run, which no longer finds them live.
func (r retired) Delete(ctx context.Context) model.GarbageDeletion {
	if r.report.Detail == nil {
		return model.GarbageDeletion{}
	}
	var (
		ops     = r.report.Detail.Findings().Ops
		deleted []model.GarbageDeleted
		failed  []model.GarbageDeleteFailure
		notRun  int
	)
	ctx, batch := progress.Batch(ctx, progress.Step{
		Kind: kindSavepointDrop,
		Name: r.report.ComposeProject.String(),
	}, len(ops))
	for i, op := range ops {
		if ctx.Err() != nil {
			notRun = len(ops) - i
			break
		}
		removeCtx, step := progress.Run(ctx, progress.Step{
			Kind: kindSavepoint,
			ID:   op.Target,
		})
		err := r.savepoints.Remove(removeCtx, model.SavepointID(op.Target))
		step.End(err)
		if err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("target", op.Target).Msg("delete garbage")
			failed = append(failed, model.GarbageDeleteFailure{
				Target:      op.Target,
				Error:       err.Error(),
				Interrupted: ctx.Err() != nil,
			})
			continue
		}
		deleted = append(deleted, model.GarbageDeleted{
			Target: op.Target,
			Bytes:  op.Bytes,
		})
	}
	deletion := model.GarbageDeletion{
		Deleted: deleted,
		Failed:  failed,
		NotRun:  notRun,
	}
	batch.End(newDeletionError(ctx, &deletion))
	return deletion
}

// readRetired reads, per compose project of the session, the safety snapshots beyond the newest
// s.retainRecent, unless dataKinds names others alone. What cannot be read aborts its report.
func (s *GarbageCollector) readRetired(ctx context.Context, dataKinds []model.GarbageDataKind) []provider.Garbage {
	if len(dataKinds) > 0 && !slices.Contains(dataKinds, model.GarbageDataKindSafetySnapshots) {
		return nil
	}
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindGCRetain})
	defer func() { step.End(context.Cause(ctx)) }()
	workloads, err := s.providerSession.Workloads(ctx)
	if err != nil {
		report := newAbortedRetention(ctx, "", fmt.Errorf("list compose projects of session %s: %w", s.sessionID, err))
		return []provider.Garbage{retired{report: report}}
	}
	found := make([]provider.Garbage, 0, len(workloads))
	for _, workload := range workloads {
		found = append(found, s.readWorkloadRetired(ctx, workload))
	}
	return found
}

// readWorkloadRetired reads the safety snapshots of workload beyond the retention.
func (s *GarbageCollector) readWorkloadRetired(ctx context.Context, workload provider.WorkloadName) retired {
	var (
		name       = model.DockerComposeProjectName(workload)
		savepoints provider.Savepoints
		listed     []model.Savepoint
		err        error
	)
	if savepoints, err = s.providerSession.Savepoints(workload); err != nil {
		return retired{report: newAbortedRetention(ctx, name, fmt.Errorf("open compose project %s: %w", name, err))}
	}
	if listed, err = savepoints.List(ctx); err != nil {
		return retired{report: newAbortedRetention(ctx, name, fmt.Errorf("read savepoints of compose project %s: %w", name, err))}
	}
	stat := newRetentionStat(listed, s.retainRecent)
	return retired{
		savepoints: savepoints,
		report:     newRetentionReport(name, stat.Summary(), stat),
	}
}

// newAbortedRetention is the report of compose project name, aborted for err, warned of on the
// step ctx carries; no name is every compose project.
func newAbortedRetention(ctx context.Context, name model.DockerComposeProjectName, err error) model.GarbageReport {
	aborted := fmt.Sprintf("cannot read the scope: %v", err)
	zerolog.Ctx(ctx).Info().Err(err).Str("compose_project", name.String()).Msg("safety snapshots not judged")
	progress.Current(ctx).WarnInResult("aborted: " + aborted)
	return newRetentionReport(name, model.GarbageSummary{Aborted: aborted}, nil)
}

// newRetentionReport is the retention's report of compose project name.
func newRetentionReport(
	name model.DockerComposeProjectName, summary model.GarbageSummary, stat model.GarbageStat,
) model.GarbageReport {
	return model.GarbageReport{
		DataKind:       model.GarbageDataKindSafetySnapshots,
		Collector:      model.GarbageCollectorKindSafetySnapshotRetention,
		ComposeProject: name,
		Summary:        summary,
		Detail:         stat,
	}
}

// newRetentionStat names every safety snapshot of savepoints past the newest retainRecent, newest
// first. Age is the id's, a UUIDv7, not CreatedAt's, another host's clock; a size is not known.
func newRetentionStat(savepoints []model.Savepoint, retainRecent int) model.SafetySnapshotStat {
	var (
		ids      []model.SavepointID
		ops      = []model.GarbageOp{}
		retained int
	)
	for _, savepoint := range savepoints {
		if savepoint.IsSafetySnapshot() {
			ids = append(ids, savepoint.ID)
		}
	}
	slices.SortFunc(ids, func(a, b model.SavepointID) int { return cmp.Compare(b, a) })

	retained = min(retainRecent, len(ids))
	for _, id := range ids[retained:] {
		ops = append(ops, model.GarbageOp{
			Action: model.GarbageActionDeleteSavepoint,
			Target: id.String(),
			Reason: model.GarbageReasonBeyondRetention,
		})
	}
	return model.SafetySnapshotStat{
		GarbageFindings: model.GarbageFindings{Ops: ops},
		Retained:        retained,
	}
}
