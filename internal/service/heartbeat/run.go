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

package heartbeat

import (
	"context"
	"errors"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// Run is the heartbeat framing one operation's tree in the store: in progress from its start
// until the operation commits, then done, or failed on any other end. Once the work it frames
// returns, the caller ends it with exactly one of Commit and Fail, Fail also after a failed Commit.
type Run struct {
	worker  *Worker
	cancel  context.CancelCauseFunc
	stopped bool
}

// StartRun starts beating base through writer. What the heartbeat frames runs on the context
// returned, cancelled with model.ErrHeartbeatLost once the heartbeat is lost. A start that fails
// is marked failed: a write that timed out may still have landed.
func StartRun(
	ctx context.Context, writer heartbeatWriter, interval, staleAfter time.Duration, base Heartbeat,
) (context.Context, *Run, error) {
	beatCtx, cancel := context.WithCancelCause(ctx)
	worker := NewWorker(writer).
		SetInterval(interval).
		SetStaleAfter(staleAfter)
	if err := worker.Start(ctx, base, cancel); err != nil {
		worker.Fail(ctx, err)
		cancel(nil)
		return nil, nil, err
	}
	return beatCtx, &Run{
		worker: worker,
		cancel: cancel,
	}, nil
}

// Stop ends the beats once what the heartbeat frames has returned; a second call does nothing.
func (r *Run) Stop() {
	if r.stopped {
		return
	}
	r.stopped = true
	r.worker.Stop()
}

// Commit ends the heartbeat of work that succeeded: it stops the beats, refuses a heartbeat whose
// last good beat is too old, then runs verify and commit, and marks it done, past an interrupt too.
// A refusal is returned as it is and marks nothing: the caller hands its cause to Fail.
func (r *Run) Commit(ctx context.Context, verify, commit func(ctx context.Context) error) error {
	r.Stop()
	if err := r.worker.Alive(); err != nil {
		return err
	}
	if err := verify(ctx); err != nil {
		return err
	}
	if err := commit(ctx); err != nil {
		return err
	}
	r.worker.Finish(context.WithoutCancel(ctx))
	r.cancel(nil)
	return nil
}

// Fail stops the beats, marks the heartbeat failed with cause and leaves the tree to a gc.
func (r *Run) Fail(ctx context.Context, cause error) {
	r.Stop()
	r.worker.Fail(ctx, cause)
	r.cancel(nil)
}

// PreferLost returns the lost heartbeat that cancelled beatCtx in place of err, the symptom it
// caused; err otherwise.
func PreferLost(beatCtx context.Context, err error) error {
	if lost := context.Cause(beatCtx); errors.Is(lost, model.ErrHeartbeatLost) {
		return lost
	}
	return err
}
