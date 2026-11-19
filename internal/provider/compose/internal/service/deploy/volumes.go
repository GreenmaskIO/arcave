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

package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// volumeRestoreOpName is what a volume's restore is called in its logs and errors.
const volumeRestoreOpName = "restore"

// RestoreVolumes writes every volume back concurrently, each carrying the payload its own
// capture reported; stats index alike with backends.
func (s *Deployer) RestoreVolumes(
	ctx context.Context, savepoint composemodel.Savepoint, backends []composemodel.VolumeSnapshotBackend,
) error {
	if err := savepoint.Header.ID.Validate(); err != nil {
		return fmt.Errorf("validate savepoint id: %w", err)
	}
	if len(backends) == 0 {
		return nil
	}

	// Before the group: the batch reports the group's cancellation, so it opens outside it.
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindVolumeRestore}, len(backends))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, backend := range backends {
		eg.Go(func() error {
			opCtx := volumeRestoreContext(egCtx, backend, savepoint.Header.ID)
			opCtx, step := progress.Run(opCtx, progress.Step{
				Kind: kindVolume,
				ID:   backend.Volume().ObjectID,
			}, progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("volume operation started")
			if err := backend.Restore(opCtx, composemodel.BackendRestoreRequest{
				SnapshotID: savepoint.Header.ID,
				Payload:    savepoint.Detail.Stats[i].Payload,
				SizeBytes:  savepoint.Detail.Stats[i].SizeBytes,
			}); err != nil {
				err = fmt.Errorf("%s %s: restore capture: %w", volumeRestoreOpName, backend.Volume().ObjectID, err)
				step.End(err)
				return err
			}
			step.Done(composemodel.NewRestoreSample(savepoint.Detail.Stats[i].SizeBytes))
			step.End(nil)
			log.Debug().Dur("duration", time.Since(started)).Msg("volume operation finished")
			return nil
		})
	}
	err := eg.Wait()
	batch.End(err)
	return err
}

// volumeRestoreContext attributes every line the restore logs.
func volumeRestoreContext(ctx context.Context, backend composemodel.VolumeSnapshotBackend, id model.SavepointID) context.Context {
	volume := backend.Volume()
	return zerolog.Ctx(ctx).With().
		Str("volume", volume.ObjectID).
		Str("backend", string(volume.Backend)).
		Str("snapshot_id", id.String()).
		Str("op", volumeRestoreOpName).
		Logger().
		WithContext(ctx)
}
