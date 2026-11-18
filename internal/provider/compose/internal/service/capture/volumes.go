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

package capture

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// volumeOpName is what a volume's capture is called in its logs and errors.
const volumeOpName = "snapshot"

// captureVolumes takes one capture per backend under id, concurrently, its stats indexed alike
// with backends; on error what the fan-out wrote is left for a later gc.
func (s *Capturer) captureVolumes(
	ctx context.Context, id model.SavepointID, labels map[string]string, backends []model.VolumeSnapshotBackend,
) ([]model.SnapshotStat, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("validate savepoint id: %w", err)
	}
	stats := make([]model.SnapshotStat, len(backends))
	if len(backends) == 0 {
		return stats, nil
	}

	// Before the group: the batch reports the group's cancellation, so it opens outside it.
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindVolumeCapture}, len(backends))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, backend := range backends {
		eg.Go(func() error {
			opCtx := volumeOpContext(egCtx, backend, id)
			opCtx, step := progress.Run(opCtx, progress.Step{
				Kind: kindVolume,
				ID:   backend.Volume().ObjectID,
			}, progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("volume operation started")
			stat, err := backend.Snapshot(opCtx, model.BackendSnapshotRequest{
				SnapshotID: id,
				Labels:     labels,
			})
			if err != nil {
				err = fmt.Errorf("%s %s: capture volume: %w", volumeOpName, backend.Volume().ObjectID, err)
				step.End(err)
				return err
			}
			stats[i] = stat
			step.Done(newCaptureSample(stat.SizeBytes, stat.StoredBytes, stat.ReusedBytes))
			step.End(nil)
			log.Debug().Dur("duration", time.Since(started)).Msg("volume operation finished")
			return nil
		})
	}
	err := eg.Wait()
	batch.End(err)
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// volumeOpContext attributes every line the capture logs.
func volumeOpContext(ctx context.Context, backend model.VolumeSnapshotBackend, id model.SavepointID) context.Context {
	volume := backend.Volume()
	return zerolog.Ctx(ctx).With().
		Str("volume", volume.ObjectID).
		Str("backend", string(volume.Backend)).
		Str("snapshot_id", id.String()).
		Str("op", volumeOpName).
		Logger().
		WithContext(ctx)
}
