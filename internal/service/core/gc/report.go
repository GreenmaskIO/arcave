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

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// newDeadSavepoints groups every op that names a dead savepoint by compose project and
// savepoint, in the order the reports first name them.
func newDeadSavepoints(reports []model.GarbageReport) []model.DeadSavepoint {
	type deadKey struct {
		composeProject model.DockerComposeProjectName
		savepoint      model.SavepointID
	}
	var dead []model.DeadSavepoint
	index := map[deadKey]int{}
	for _, report := range reports {
		if report.Detail == nil {
			continue
		}
		for _, op := range report.Detail.Findings().Ops {
			if op.Savepoint == "" {
				continue
			}
			key := deadKey{
				composeProject: report.ComposeProject,
				savepoint:      op.Savepoint,
			}
			i, seen := index[key]
			if !seen {
				i, index[key] = len(dead), len(dead)
				dead = append(dead, model.DeadSavepoint{
					ComposeProject: key.composeProject,
					Savepoint:      key.savepoint,
					Bytes:          new(int64),
				})
			}
			dead[i].Found++
			if op.Bytes == nil || dead[i].Bytes == nil {
				dead[i].Bytes = nil
				continue
			}
			*dead[i].Bytes += *op.Bytes
		}
	}
	return dead
}

// newGarbageTotal adds up reports; bytes stay complete only while every report knew its own, and
// freed bytes are known only once a report carries a deletion and every deleted size is.
func newGarbageTotal(reports []model.GarbageReport) model.GarbageTotal {
	var (
		found, skipped, unjudged      int
		aborted, failed               int
		deleted, deleteFailed, notRun int
		bytes                         int64
		bytesComplete                 = true
		freedBytes                    *int64
		freedUnknown                  bool
	)
	for _, report := range reports {
		if deletion := report.Deletion; deletion != nil {
			deleted += len(deletion.Deleted)
			deleteFailed += len(deletion.Failed)
			notRun += deletion.NotRun
			if freed := deletion.FreedBytes(); freed != nil {
				freedBytes = utils.New(utils.Value(freedBytes) + *freed)
			} else {
				freedUnknown = true
			}
		}
		switch {
		case report.Error != "":
			failed++
			bytesComplete = false
			continue
		case report.Summary.Aborted != "":
			aborted++
			bytesComplete = false
		}
		found += report.Summary.Found
		skipped += report.Summary.Skipped
		unjudged += report.Summary.Unjudged
		if report.Summary.Bytes == nil {
			bytesComplete = false
			continue
		}
		bytes += *report.Summary.Bytes
	}
	if freedUnknown {
		freedBytes = nil
	}
	return model.GarbageTotal{
		Found:         found,
		Bytes:         bytes,
		BytesComplete: bytesComplete,
		Skipped:       skipped,
		Unjudged:      unjudged,
		Aborted:       aborted,
		Failed:        failed,
		Deleted:       deleted,
		DeleteFailed:  deleteFailed,
		NotRun:        notRun,
		FreedBytes:    freedBytes,
	}
}

// logGarbageFound logs what reports found, each and summed, before any deletion.
func logGarbageFound(ctx context.Context, reports []model.GarbageReport) {
	log := zerolog.Ctx(ctx)
	for _, report := range reports {
		event := log.Info().
			Str("data_kind", string(report.DataKind)).
			Str("compose_project", report.ComposeProject.String()).
			Int("found", report.Summary.Found).
			Bool("bytes_complete", report.Summary.Bytes != nil)
		if report.Summary.Bytes != nil {
			event = event.Int64("bytes", *report.Summary.Bytes)
		}
		event.Msg("garbage found")
	}
	total := newGarbageTotal(reports)
	log.Info().
		Int("found", total.Found).
		Int64("bytes", total.Bytes).
		Bool("bytes_complete", total.BytesComplete).
		Msg("garbage found in the scope")
}
