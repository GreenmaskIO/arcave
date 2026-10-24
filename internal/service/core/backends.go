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

package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// newBackends builds one backend per volume, all rooted at the one store, in
// input order. All up front: an unregistered backend fails before any byte moves.
func (s *Service) newBackends(volumes []model.Volume) ([]model.VolumeSnapshotBackend, error) {
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

// captureVolumes takes one capture per backend under id, its stats indexed alike with backends;
// on error what the fan-out wrote is left for a later gc.
func (s *Service) captureVolumes(
	ctx context.Context, id model.SavepointID, labels map[string]string, backends []model.VolumeSnapshotBackend,
) ([]model.SnapshotStat, error) {
	stats := make([]model.SnapshotStat, len(backends))
	err := s.forEachBackend(ctx, id, backends, "snapshot",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			stat, err := backend.Snapshot(ctx, model.BackendSnapshotRequest{
				SnapshotID: id,
				Labels:     labels,
			})
			if err != nil {
				return fmt.Errorf("capture volume: %w", err)
			}
			stats[i] = stat
			return nil
		})
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// performSavepoint captures a fresh savepoint under a heartbeat and commits its record last;
// on failure it returns a zero savepoint. killed are the containers the stop killed.
func (s *Service) performSavepoint(
	ctx context.Context,
	working workingSet,
	killed []string,
	name string,
	description string,
	labels map[string]string,
) (model.Savepoint, error) {
	var (
		id        = model.NewSavepointID()
		createdAt = time.Now()
		writer    heartbeatWriter
		captured  capturedSavepoint
		err       error
	)
	if err = s.requireEmptyTree(ctx, id); err != nil {
		return model.Savepoint{}, err
	}
	if writer, err = s.openHeartbeat(id); err != nil {
		return model.Savepoint{}, fmt.Errorf("savepoint %s: open the heartbeat: %w", id, err)
	}
	captureCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	worker := heartbeat.NewWorker(writer).SetInterval(s.heartbeatInterval).SetStaleAfter(s.heartbeatStaleAfter)
	initialBeat := heartbeat.Heartbeat{
		Operation:  model.HeartbeatOperationSavepoint,
		RunnerInfo: s.runnerInfo,
	}
	if err = worker.Start(ctx, initialBeat, cancel); err != nil {
		// A write that timed out may still have landed: mark it failed rather than leave it in progress.
		err = fmt.Errorf("savepoint %s: %w", id, err)
		abandonCapture(ctx, worker, id, err)
		return model.Savepoint{}, err
	}

	captured, err = s.captureSavepoint(captureCtx, id, labels, working)
	worker.Stop()
	if err != nil {
		if cause := context.Cause(captureCtx); errors.Is(cause, model.ErrHeartbeatLost) {
			err = cause
		}
		abandonCapture(ctx, worker, id, err)
		return model.Savepoint{}, err
	}
	if err = worker.Alive(); err != nil {
		err = fmt.Errorf("commit savepoint %s: %w", id, err)
		abandonCapture(ctx, worker, id, err)
		return model.Savepoint{}, err
	}
	savepoint := model.Savepoint{
		ID:             id,
		Name:           name,
		Description:    description,
		Labels:         labels,
		CreatedAt:      createdAt,
		Stats:          captured.stats,
		Images:         captured.images,
		WritableLayers: captured.writableLayers,
		Spec:           &model.SpecRef{Format: model.SpecFormatComposeYAML},
		NotCovered:     working.newNotCovered(killed),
	}
	if err = s.savepointRepo.SaveSavepoint(ctx, savepoint); err != nil {
		err = fmt.Errorf("commit savepoint %s: %w", id, err)
		abandonCapture(ctx, worker, id, err)
		return model.Savepoint{}, err
	}
	worker.Finish(ctx)
	return savepoint, nil
}

// openHeartbeat opens the heartbeat at the root of id's tree.
func (s *Service) openHeartbeat(id model.SavepointID) (heartbeatWriter, error) {
	tree, err := s.store.SubStorage(id.String(), true)
	if err != nil {
		return nil, err
	}
	return heartbeat.NewWriter(tree), nil
}

// capturedSavepoint is what a capture stored under its id, short of the record.
type capturedSavepoint struct {
	stats          []model.SnapshotStat
	images         []model.Image
	writableLayers []model.WritableLayer
}

// captureSavepoint stores everything a savepoint holds but its record: volumes, images, layers,
// and the spec written beside them.
func (s *Service) captureSavepoint(
	ctx context.Context, id model.SavepointID, labels map[string]string, working workingSet,
) (capturedSavepoint, error) {
	var (
		stats          []model.SnapshotStat
		images         []model.Image
		writableLayers []model.WritableLayer
		err            error
	)
	if stats, err = s.captureVolumes(ctx, id, labels, working.backends); err != nil {
		return capturedSavepoint{}, err
	}
	// The images first: a writable layer's stored_bytes is then what it adds over its image.
	if images, err = s.captureImages(ctx, id, working.images.upstreams); err != nil {
		return capturedSavepoint{}, err
	}
	if writableLayers, err = s.captureWritableLayers(ctx, id, working.images.writableLayers); err != nil {
		return capturedSavepoint{}, err
	}
	if err = s.savepointRepo.SaveSpec(ctx, id, working.spec); err != nil {
		return capturedSavepoint{}, fmt.Errorf("write compose spec of savepoint %s: %w", id, err)
	}
	return capturedSavepoint{
		stats:          stats,
		images:         images,
		writableLayers: writableLayers,
	}, nil
}

// abandonCapture marks an uncommitted capture's heartbeat failed and logs its tree for a gc.
func abandonCapture(ctx context.Context, worker *heartbeat.Worker, id model.SavepointID, cause error) {
	worker.Fail(ctx, cause)
	zerolog.Ctx(ctx).Warn().Str("snapshot_id", id.String()).
		Msg("the partial capture is retained for a later gc")
}

// restoreVolumes writes every volume back, each carrying the payload its own
// capture reported; stats index alike with backends.
func (s *Service) restoreVolumes(
	ctx context.Context, savepoint model.Savepoint, backends []model.VolumeSnapshotBackend,
) error {
	return s.forEachBackend(ctx, savepoint.ID, backends, "restore",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			if err := backend.Restore(ctx, model.BackendRestoreRequest{
				SnapshotID: savepoint.ID,
				Payload:    savepoint.Stats[i].Payload,
			}); err != nil {
				return fmt.Errorf("restore capture: %w", err)
			}
			return nil
		})
}

// dropSavepoint uncommits the record first, then drops the data it named.
// Reversing that leaves a savepoint that still lists but is half gone, so the
// order is pinned by a test.
//
// It is also what a failed capture wants: Delete is idempotent — a namespace
// with no record is already uncommitted — so one function serves both.
//
// The sweep runs whatever the backends reported: it is what reclaims an archive a
// capture that died mid-fan-out left behind, which no stat names.
func (s *Service) dropSavepoint(
	ctx context.Context,
	savepoint model.Savepoint,
	backends []model.VolumeSnapshotBackend,
) error {
	if err := s.savepointRepo.DeleteSavepoint(ctx, savepoint.ID); err != nil {
		return fmt.Errorf("uncommit savepoint %s: %w", savepoint.ID, err)
	}

	removeErr := s.forEachBackend(ctx, savepoint.ID, backends, "remove",
		func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error {
			if err := backend.Remove(ctx, model.BackendRemoveRequest{
				SnapshotID: savepoint.ID,
				Payload:    savepoint.Stats[i].Payload,
			}); err != nil {
				return fmt.Errorf("remove capture: %w", err)
			}
			return nil
		})

	sweepErr := s.sweepTree(ctx, savepoint.ID)
	if sweepErr != nil {
		sweepErr = fmt.Errorf("remove capture directory %s: %w", savepoint.ID, sweepErr)
	}
	return errors.Join(removeErr, sweepErr)
}

// forEachBackend runs op over every backend concurrently and waits for all of
// them, so the caller's next group starts only once this one finished
// everywhere. op gets the index to write into a caller-preallocated slice.
// snapshotID is validated here once, ahead of every dispatch.
func (s *Service) forEachBackend(
	ctx context.Context,
	snapshotID model.SavepointID,
	backends []model.VolumeSnapshotBackend,
	opName string,
	op func(ctx context.Context, i int, backend model.VolumeSnapshotBackend) error,
) error {
	if err := snapshotID.Validate(); err != nil {
		return fmt.Errorf("validate savepoint id: %w", err)
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, backend := range backends {
		eg.Go(func() error {
			opCtx := volumeOpContext(egCtx, backend, snapshotID, opName)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Info().Msg("volume operation started")
			if err := op(opCtx, i, backend); err != nil {
				return fmt.Errorf("%s %s: %w", opName, backend.Volume().ObjectID, err)
			}
			log.Info().Dur("duration", time.Since(started)).Msg("volume operation finished")
			return nil
		})
	}
	return eg.Wait()
}

// volumeOpContext attributes every line the operation logs.
func volumeOpContext(
	ctx context.Context, backend model.VolumeSnapshotBackend, snapshotID model.SavepointID, opName string,
) context.Context {
	volume := backend.Volume()
	return zerolog.Ctx(ctx).With().
		Str("volume", volume.ObjectID).
		Str("backend", string(volume.Backend)).
		Str("snapshot_id", snapshotID.String()).
		Str("op", opName).
		Logger().
		WithContext(ctx)
}

// requireEmptyTree refuses an id whose capture directory already holds objects: an
// interrupted run's remains, possibly the last copy of what it captured.
func (s *Service) requireEmptyTree(ctx context.Context, id model.SavepointID) error {
	objects, err := s.store.List(ctx, id.String())
	if err != nil {
		return fmt.Errorf("reading the capture directory %s: %w", id, err)
	}
	if len(objects) > 0 {
		return fmt.Errorf("%w: %s holds %d object(s), an interrupted run's remains; check what is there before removing it",
			errCaptureDirectoryNotEmpty, id, len(objects))
	}
	return nil
}

// sweepTree drops a whole capture directory — the one storage key the service
// addresses itself; everything below it is the backends'. An absent tree is not
// an error.
func (s *Service) sweepTree(ctx context.Context, id model.SavepointID) error {
	if err := s.store.DeleteAll(ctx, id.String()); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return err
	}
	return nil
}
