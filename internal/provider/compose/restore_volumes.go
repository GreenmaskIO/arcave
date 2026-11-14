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

package compose

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// volumeRestoreOpName is what a volume's restore is called in its logs and errors.
const volumeRestoreOpName = "restore"

// restoreVolumes writes every volume back concurrently, each carrying the payload its own
// capture reported; stats index alike with backends.
func (s *Provider) restoreVolumes(
	ctx context.Context, savepoint model.Savepoint, backends []model.VolumeSnapshotBackend,
) error {
	if err := savepoint.ID.Validate(); err != nil {
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
			opCtx := volumeRestoreContext(egCtx, backend, savepoint.ID)
			opCtx, step := progress.Run(opCtx, progress.Step{
				Kind: kindVolume,
				ID:   backend.Volume().ObjectID,
			}, progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("volume operation started")
			if err := backend.Restore(opCtx, model.BackendRestoreRequest{
				SnapshotID: savepoint.ID,
				Payload:    savepoint.Stats[i].Payload,
				SizeBytes:  savepoint.Stats[i].SizeBytes,
			}); err != nil {
				err = fmt.Errorf("%s %s: restore capture: %w", volumeRestoreOpName, backend.Volume().ObjectID, err)
				step.End(err)
				return err
			}
			step.Done(newRestoreSample(savepoint.Stats[i].SizeBytes))
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
func volumeRestoreContext(ctx context.Context, backend model.VolumeSnapshotBackend, id model.SavepointID) context.Context {
	volume := backend.Volume()
	return zerolog.Ctx(ctx).With().
		Str("volume", volume.ObjectID).
		Str("backend", string(volume.Backend)).
		Str("snapshot_id", id.String()).
		Str("op", volumeRestoreOpName).
		Logger().
		WithContext(ctx)
}

// newBackends builds one backend per volume, all rooted at the one store, in
// input order. All up front: an unregistered backend fails before any byte moves.
func (s *Provider) newBackends(volumes []model.Volume) ([]model.VolumeSnapshotBackend, error) {
	backends := make([]model.VolumeSnapshotBackend, len(volumes))
	for i, volume := range volumes {
		backend, err := s.snapshotBackendRegistry.New(model.BackendSpec{
			Store:  s.store,
			Volume: volume,
		})
		if err != nil {
			return nil, fmt.Errorf("building %s backend for volume %s: %w", volume.Backend, volume.ObjectID, err)
		}
		backends[i] = backend
	}
	return backends, nil
}
