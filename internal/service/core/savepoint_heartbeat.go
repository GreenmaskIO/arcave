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

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// errCaptureDirectoryNotEmpty refuses to capture into a directory that already holds data.
var errCaptureDirectoryNotEmpty = errors.New("capture directory is not empty")

// savepointHeartbeat is the heartbeat at the root of one savepoint's tree: in progress from before
// the hold until the record is written, then done, or failed on any other end.
type savepointHeartbeat struct {
	id     model.SavepointID
	worker *heartbeat.Worker
	cancel context.CancelCauseFunc
}

// startSavepointHeartbeat refuses id's tree if it holds anything, then starts its heartbeat. What
// the heartbeat frames runs on the context returned, cancelled with model.ErrHeartbeatLost once
// the heartbeat is lost.
//
// TODO: core knows the savepoint's tree — its path under the compose project and that it must be
// empty — which is the provider's layout. Move the "where" behind a stage (the provider checks the
// tree and hands back the heartbeat's writer) and keep only the lifecycle here.
func (s *Service) startSavepointHeartbeat(
	ctx context.Context, id model.SavepointID,
) (context.Context, savepointHeartbeat, error) {
	tree, err := storefactory.Savepoint(s.store, id)
	if err != nil {
		return nil, savepointHeartbeat{}, fmt.Errorf("savepoint %s: %w", id, err)
	}
	if err = requireEmptyTree(ctx, tree, id); err != nil {
		return nil, savepointHeartbeat{}, err
	}
	beatCtx, cancel := context.WithCancelCause(ctx)
	worker := heartbeat.NewWorker(heartbeat.NewWriter(tree)).
		SetInterval(s.heartbeatInterval).
		SetStaleAfter(s.heartbeatStaleAfter)
	initialBeat := heartbeat.Heartbeat{
		Operation:  model.HeartbeatOperationSavepoint,
		RunnerInfo: s.runnerInfo,
	}
	if err = worker.Start(ctx, initialBeat, cancel); err != nil {
		// A write that timed out may still have landed: mark it failed rather than leave it in progress.
		err = fmt.Errorf("savepoint %s: %w", id, err)
		markCaptureFailed(ctx, worker, id, err)
		cancel(nil)
		return nil, savepointHeartbeat{}, err
	}
	return beatCtx, savepointHeartbeat{
		id:     id,
		worker: worker,
		cancel: cancel,
	}, nil
}

// stop ends the beats once what the heartbeat frames has returned; call it exactly once.
func (b savepointHeartbeat) stop() {
	b.worker.Stop()
}

// alive refuses a commit once the stopped heartbeat's last good beat is too old.
func (b savepointHeartbeat) alive() error {
	if err := b.worker.Alive(); err != nil {
		return fmt.Errorf("commit savepoint %s: %w", b.id, err)
	}
	return nil
}

// finish marks the stopped heartbeat done, its record written: past an interrupt too, which must
// not leave it in progress.
func (b savepointHeartbeat) finish(ctx context.Context) {
	b.worker.Finish(context.WithoutCancel(ctx))
	b.cancel(nil)
}

// abandon ends the heartbeat of a capture that never ran, its hold failed; see fail.
func (b savepointHeartbeat) abandon(ctx, beatCtx context.Context, cause error) error {
	b.worker.Stop()
	return b.fail(ctx, beatCtx, cause)
}

// fail marks the stopped heartbeat failed and leaves the tree to a gc. A lost heartbeat is
// returned in place of cause, the symptom it caused.
func (b savepointHeartbeat) fail(ctx, beatCtx context.Context, cause error) error {
	if lost := context.Cause(beatCtx); errors.Is(lost, model.ErrHeartbeatLost) {
		cause = lost
	}
	markCaptureFailed(ctx, b.worker, b.id, cause)
	b.cancel(nil)
	return cause
}

// requireEmptyTree refuses a savepoint's tree that already holds objects: an interrupted run's
// remains, possibly the last copy of what it captured.
func requireEmptyTree(ctx context.Context, tree storages.Inspector, id model.SavepointID) error {
	objects, err := tree.List(ctx, "")
	if err != nil {
		return fmt.Errorf("reading the capture directory %s: %w", id, err)
	}
	if len(objects) > 0 {
		return fmt.Errorf("%w: %s holds %d object(s), an interrupted run's remains; check what is there before removing it",
			errCaptureDirectoryNotEmpty, id, len(objects))
	}
	return nil
}

// markCaptureFailed marks an uncommitted capture's heartbeat failed and logs its tree for a gc.
func markCaptureFailed(ctx context.Context, worker *heartbeat.Worker, id model.SavepointID, cause error) {
	worker.Fail(ctx, cause)
	zerolog.Ctx(ctx).Warn().Str("snapshot_id", id.String()).
		Msg("the partial capture is retained for a later gc")
}
