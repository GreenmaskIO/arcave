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
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// operationHeartbeat is the heartbeat framing one operation's tree in the store: in progress from
// its start until the operation commits, then done, or failed on any other end.
type operationHeartbeat struct {
	worker *heartbeat.Worker
	cancel context.CancelCauseFunc
}

// startOperationHeartbeat starts beating initialBeat through writer. What the heartbeat frames runs
// on the context returned, cancelled with model.ErrHeartbeatLost once the heartbeat is lost. A
// start that fails is marked failed: a write that timed out may still have landed.
func startOperationHeartbeat(
	ctx context.Context, writer heartbeatWriter, interval, staleAfter time.Duration, initialBeat heartbeat.Heartbeat,
) (context.Context, operationHeartbeat, error) {
	beatCtx, cancel := context.WithCancelCause(ctx)
	worker := heartbeat.NewWorker(writer).
		SetInterval(interval).
		SetStaleAfter(staleAfter)
	if err := worker.Start(ctx, initialBeat, cancel); err != nil {
		worker.Fail(ctx, err)
		cancel(nil)
		return nil, operationHeartbeat{}, err
	}
	return beatCtx, operationHeartbeat{
		worker: worker,
		cancel: cancel,
	}, nil
}

// stop ends the beats once what the heartbeat frames has returned; call it exactly once.
func (b operationHeartbeat) stop() {
	b.worker.Stop()
}

// alive refuses a commit once the stopped heartbeat's last good beat is too old.
func (b operationHeartbeat) alive() error {
	return b.worker.Alive()
}

// finish marks the stopped heartbeat done, its record written: past an interrupt too, which must
// not leave it in progress.
func (b operationHeartbeat) finish(ctx context.Context) {
	b.worker.Finish(context.WithoutCancel(ctx))
	b.cancel(nil)
}

// fail marks the stopped heartbeat failed and leaves the tree to a gc. A lost heartbeat is
// returned in place of cause, the symptom it caused.
func (b operationHeartbeat) fail(ctx, beatCtx context.Context, cause error) error {
	if lost := context.Cause(beatCtx); errors.Is(lost, model.ErrHeartbeatLost) {
		cause = lost
	}
	b.worker.Fail(ctx, cause)
	b.cancel(nil)
	return cause
}
