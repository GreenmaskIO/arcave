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
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// errCaptureDirectoryNotEmpty refuses to capture into a directory that already holds data.
var errCaptureDirectoryNotEmpty = errors.New("capture directory is not empty")

// capturedSavepoint is what a capture stored under its id, short of the record.
type capturedSavepoint struct {
	stats          []model.SnapshotStat
	images         []model.Image
	writableLayers []model.WritableLayer
}

// Capture captures and commits a savepoint of an already stopped project, the record last;
// killed are the containers the stop killed. On error the record is zero.
func (s *Service) Capture(
	ctx context.Context, grantedLock model.GrantedSessionLock, plan Plan, killed []string, req Request,
) (_ model.Savepoint, err error) {
	var (
		id         = model.NewSavepointID()
		createdAt  = time.Now()
		notCovered = plan.newNotCovered(killed)
		writer     *heartbeat.Writer
		captured   capturedSavepoint
	)
	// Outside the heartbeat's cancellation, which the step reports with its cause.
	ctx, step := progress.Begin(ctx, progress.Step{
		Kind: kindSavepointCapture,
		Name: cmp.Or(req.Name, id.String()),
	})
	defer func() { step.End(err) }()
	if warning := newNotCoveredWarning(notCovered); warning != "" {
		warn(step, warning, isInResult(req.Labels))
	}
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

	captured, err = s.captureSavepoint(captureCtx, id, req.Labels, plan)
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
	if err = grantedLock.Verify(ctx); err != nil {
		err = fmt.Errorf("commit savepoint %s: %w", id, err)
		abandonCapture(ctx, worker, id, err)
		return model.Savepoint{}, err
	}
	savepoint := model.Savepoint{
		ID:                    id,
		Name:                  req.Name,
		Description:           req.Description,
		Labels:                req.Labels,
		CreatedAt:             createdAt,
		TakenBeforeRollbackTo: req.TakenBefore,
		Stats:                 captured.stats,
		Images:                captured.images,
		WritableLayers:        captured.writableLayers,
		Spec:                  &model.SpecRef{Format: model.SpecFormatComposeYAML},
		NotCovered:            notCovered,
	}
	if err = s.savepointRepo.SaveSavepoint(ctx, savepoint); err != nil {
		err = fmt.Errorf("commit savepoint %s: %w", id, err)
		abandonCapture(ctx, worker, id, err)
		return model.Savepoint{}, err
	}
	worker.Finish(ctx)
	return savepoint, nil
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

// openHeartbeat opens the heartbeat at the root of id's tree.
func (s *Service) openHeartbeat(id model.SavepointID) (*heartbeat.Writer, error) {
	tree, err := s.store.SubStorage(id.String(), true)
	if err != nil {
		return nil, err
	}
	return heartbeat.NewWriter(tree), nil
}

// abandonCapture marks an uncommitted capture's heartbeat failed and logs its tree for a gc.
func abandonCapture(ctx context.Context, worker *heartbeat.Worker, id model.SavepointID, cause error) {
	worker.Fail(ctx, cause)
	zerolog.Ctx(ctx).Warn().Str("snapshot_id", id.String()).
		Msg("the partial capture is retained for a later gc")
}

// captureSavepoint stores everything a savepoint holds but its record: volumes, images, layers,
// and the spec written beside them.
func (s *Service) captureSavepoint(
	ctx context.Context, id model.SavepointID, labels map[string]string, plan Plan,
) (capturedSavepoint, error) {
	var (
		stats          []model.SnapshotStat
		images         []model.Image
		writableLayers []model.WritableLayer
		err            error
	)
	if stats, err = s.captureVolumes(ctx, id, labels, plan.Volumes); err != nil {
		return capturedSavepoint{}, err
	}
	// The images first: a writable layer's stored_bytes is then what it adds over its image.
	if images, err = s.captureImages(ctx, id, plan.Images, isInResult(labels)); err != nil {
		return capturedSavepoint{}, err
	}
	if writableLayers, err = s.captureWritableLayers(ctx, id, plan.WritableLayers); err != nil {
		return capturedSavepoint{}, err
	}
	if err = s.savepointRepo.SaveSpec(ctx, id, plan.Spec); err != nil {
		return capturedSavepoint{}, fmt.Errorf("write compose spec of savepoint %s: %w", id, err)
	}
	return capturedSavepoint{
		stats:          stats,
		images:         images,
		writableLayers: writableLayers,
	}, nil
}
