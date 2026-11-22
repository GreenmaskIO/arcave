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

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// errCaptureDirectoryNotEmpty refuses to capture into a directory that already holds data.
var errCaptureDirectoryNotEmpty = errors.New("capture directory is not empty")

// savepointHeartbeat is the heartbeat at the root of one savepoint's tree: in progress from before
// the hold until the record is written, then done, or failed on any other end.
type savepointHeartbeat struct {
	run *heartbeat.Run
	id  model.SavepointID
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
	initialBeat := heartbeat.Heartbeat{
		Operation:  model.HeartbeatOperationSavepoint,
		RunnerInfo: s.runnerInfo,
	}
	beatCtx, run, err := heartbeat.StartRun(ctx, heartbeat.NewWriter(tree),
		s.heartbeatInterval, s.heartbeatStaleAfter, initialBeat)
	if err != nil {
		return nil, savepointHeartbeat{}, newCaptureRetainedError(id, err)
	}
	return beatCtx, savepointHeartbeat{
		run: run,
		id:  id,
	}, nil
}

// fail stops the beats, marks the heartbeat failed and leaves the tree to a gc, which the caller's
// error names (newCaptureRetainedError). A lost heartbeat is returned in place of cause, the symptom
// it caused.
func (b savepointHeartbeat) fail(ctx, beatCtx context.Context, cause error) error {
	b.run.Stop()
	cause = heartbeat.PreferLost(beatCtx, cause)
	b.run.Fail(ctx, cause)
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

// newCaptureRetainedError is err of the uncommitted capture id, whose tree is left for a gc; added
// past preferInterrupt, so an interrupt keeps it.
func newCaptureRetainedError(id model.SavepointID, err error) error {
	return fmt.Errorf("%w; partial capture %s is retained for a later gc", err, id)
}
