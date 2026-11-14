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
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/provider"
)

// prepare inspects the workload and plans a savepoint's capture of it, before anything is touched.
func (s *Service) prepare(ctx context.Context) (provider.CapturePlan, error) {
	live, err := s.workload.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	return live.PlanCapture(ctx, provider.CaptureOptions{})
}

// revertContext strips ctx of its cancellation and bounds it by revertBudget instead, for a
// step that puts the environment back and so must outlive a Ctrl-C.
func revertContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), revertBudget)
}

// releaseHeld sets what plan holds running again on a revertContext, whether the hold ran or not.
func releaseHeld(ctx context.Context, plan provider.CapturePlan, waitTimeout time.Duration) error {
	revertCtx, cancel := revertContext(ctx)
	defer cancel()
	return plan.Release(revertCtx, waitTimeout)
}

// logInterrupt tells the user what arcave does about an interrupt, when ctx was interrupted.
func logInterrupt(ctx context.Context, action string) {
	if ctx.Err() == nil {
		return
	}
	zerolog.Ctx(ctx).Info().Msg(action)
}

// preferInterrupt returns the interrupt that cancelled ctx in place of err, the symptom
// it caused somewhere downstream, and logs err.
func preferInterrupt(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	zerolog.Ctx(ctx).Debug().Err(err).Msg("failure the interrupt caused")
	// The cause, not ctx.Err: a lost session lock interrupts too, and says so.
	return fmt.Errorf("interrupted: %w", context.Cause(ctx))
}
