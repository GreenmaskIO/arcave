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
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
)

// eraseGarbage deletes what found found, in order, until a Verify refuses or ctx is done; when that
// left an op not run, it returns why.
func eraseGarbage(
	ctx context.Context, grantedLock model.GrantedSessionLock, found []provider.Garbage,
) ([]model.GarbageReport, error) {
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var (
		deleted = make([]model.GarbageReport, 0, len(found))
		stopped bool
	)
	for _, garbage := range found {
		deletion, err := deleteFound(ctx, grantedLock, garbage)
		if err != nil {
			stop(err)
		}
		stopped = stopped || deletion != nil && deletion.IsInterrupted()
		deleted = append(deleted, withDeletion(garbage.Report(), deletion))
	}
	if !stopped {
		// An interrupt past the last deletion cut nothing short.
		return deleted, nil
	}
	return deleted, context.Cause(ctx)
}

// deleteFound is what deleting garbage's ops did: nil when its report was aborted or failed, every
// op not run once ctx is done. A Verify that refuses is returned.
func deleteFound(
	ctx context.Context, grantedLock model.GrantedSessionLock, garbage provider.Garbage,
) (*model.GarbageDeletion, error) {
	report := garbage.Report()
	if report.Detail == nil || report.Summary.Aborted != "" {
		return nil, nil
	}
	ops := report.Detail.Findings().Ops
	switch {
	case len(ops) == 0:
		return &model.GarbageDeletion{}, nil
	case ctx.Err() != nil:
		return &model.GarbageDeletion{NotRun: len(ops)}, nil
	}
	if err := grantedLock.Verify(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("data_kind", string(report.DataKind)).
			Msg("session lock no longer vouches for the gc: nothing more is deleted")
		return &model.GarbageDeletion{NotRun: len(ops)}, fmt.Errorf("verify session lock: %w", err)
	}
	deletion := garbage.Delete(ctx)
	return &deletion, nil
}

// withDeletion is report with what deleting its ops did.
func withDeletion(report model.GarbageReport, deletion *model.GarbageDeletion) model.GarbageReport {
	report.Deletion = deletion
	return report
}
