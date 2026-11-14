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
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/captureplan"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// errCaptureDirectoryNotEmpty refuses to capture into a directory that already holds data.
var errCaptureDirectoryNotEmpty = errors.New("capture directory is not empty")

// storedSavepoint is what a capture stored under its id, short of the record.
type storedSavepoint struct {
	stats          []model.SnapshotStat
	images         []model.Image
	writableLayers []model.WritableLayer
}

// capturedSavepoint is a capture stored and not committed: its record, written by Commit, and the
// heartbeat that frames it.
type capturedSavepoint struct {
	capturer  *Capturer
	worker    *heartbeat.Worker
	savepoint model.Savepoint
}

// ID is the savepoint's id.
func (c capturedSavepoint) ID() model.SavepointID {
	return c.savepoint.ID
}

// Commit writes the record, the last object, and marks the heartbeat done; a failed write
// abandons the capture.
func (c capturedSavepoint) Commit(ctx context.Context) (model.Savepoint, error) {
	if err := c.capturer.savepointRepo.SaveSavepoint(ctx, c.savepoint); err != nil {
		err = fmt.Errorf("commit savepoint %s: %w", c.savepoint.ID, err)
		c.Abandon(ctx, err)
		return model.Savepoint{}, err
	}
	c.worker.Finish(ctx)
	return c.savepoint, nil
}

// Abandon marks the heartbeat failed with cause and leaves what was stored to a gc.
func (c capturedSavepoint) Abandon(ctx context.Context, cause error) {
	abandonCapture(ctx, c.worker, c.savepoint.ID, cause)
}

// Capture stores a savepoint of a project already held still, short of its record; killed are the
// containers the stop killed. On error the handle is nil, the capture abandoned once its heartbeat
// started.
func (s *Capturer) Capture(
	ctx context.Context, plan captureplan.Plan, killed []string, req provider.CaptureRequest,
) (_ provider.CapturedSavepoint, err error) {
	var (
		id         = model.NewSavepointID()
		createdAt  = time.Now()
		notCovered = plan.NewNotCovered(killed)
		writer     *heartbeat.Writer
		stored     storedSavepoint
	)
	// Outside the heartbeat's cancellation, which the step reports with its cause.
	ctx, step := progress.Begin(ctx, progress.Step{
		Kind: kindSavepointCapture,
		Name: cmp.Or(req.Name, id.String()),
	})
	defer func() { step.End(err) }()
	if warning := newNotCoveredWarning(notCovered); warning != "" {
		warn(step, warning, req.InResult)
	}
	if err = s.requireEmptyTree(ctx, id); err != nil {
		return nil, err
	}
	if writer, err = s.openHeartbeat(id); err != nil {
		return nil, fmt.Errorf("savepoint %s: open the heartbeat: %w", id, err)
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
		return nil, err
	}

	stored, err = s.captureSavepoint(captureCtx, id, req, plan)
	worker.Stop()
	if err != nil {
		if cause := context.Cause(captureCtx); errors.Is(cause, model.ErrHeartbeatLost) {
			err = cause
		}
		abandonCapture(ctx, worker, id, err)
		return nil, err
	}
	if err = worker.Alive(); err != nil {
		err = fmt.Errorf("commit savepoint %s: %w", id, err)
		abandonCapture(ctx, worker, id, err)
		return nil, err
	}
	return capturedSavepoint{
		capturer: s,
		worker:   worker,
		savepoint: model.Savepoint{
			ID:                    id,
			Name:                  req.Name,
			Description:           req.Description,
			Labels:                req.Labels,
			CreatedAt:             createdAt,
			TakenBeforeRollbackTo: req.TakenBefore,
			Stats:                 stored.stats,
			Images:                stored.images,
			WritableLayers:        stored.writableLayers,
			Spec:                  &model.SpecRef{Format: model.SpecFormatComposeYAML},
			NotCovered:            notCovered,
		},
	}, nil
}

// requireEmptyTree refuses an id whose capture directory already holds objects: an
// interrupted run's remains, possibly the last copy of what it captured.
func (s *Capturer) requireEmptyTree(ctx context.Context, id model.SavepointID) error {
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
func (s *Capturer) openHeartbeat(id model.SavepointID) (*heartbeat.Writer, error) {
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
func (s *Capturer) captureSavepoint(
	ctx context.Context, id model.SavepointID, req provider.CaptureRequest, plan captureplan.Plan,
) (storedSavepoint, error) {
	var (
		stats          []model.SnapshotStat
		images         []model.Image
		writableLayers []model.WritableLayer
		err            error
	)
	if stats, err = s.captureVolumes(ctx, id, req.Labels, plan.Volumes); err != nil {
		return storedSavepoint{}, err
	}
	// The images first: a writable layer's stored_bytes is then what it adds over its image.
	if images, err = s.captureImages(ctx, id, plan.Images, req.InResult); err != nil {
		return storedSavepoint{}, err
	}
	if writableLayers, err = s.captureWritableLayers(ctx, id, plan.WritableLayers); err != nil {
		return storedSavepoint{}, err
	}
	if err = s.savepointRepo.SaveSpec(ctx, id, plan.Spec); err != nil {
		return storedSavepoint{}, fmt.Errorf("write compose spec of savepoint %s: %w", id, err)
	}
	return storedSavepoint{
		stats:          stats,
		images:         images,
		writableLayers: writableLayers,
	}, nil
}
