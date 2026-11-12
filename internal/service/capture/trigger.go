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
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// captureTrigger is what fires on a group of services before a capture, and back after it.
// afterCapture must converge: it runs whether beforeCapture did or not.
type captureTrigger interface {
	beforeCapture(ctx context.Context) (killed []string, err error)
	afterCapture(revertCtx context.Context) error
}

// captureTriggers are the triggers of one capture, in the order beforeCapture fires them.
type captureTriggers []captureTrigger

// newCaptureTriggers builds the triggers of plan.
func newCaptureTriggers(composeRunner composeRunner, plan Plan) captureTriggers {
	return captureTriggers{
		stopTrigger{
			composeRunner: composeRunner,
			project:       plan.Project,
			waitTimeout:   plan.WaitTimeout,
		},
	}
}

// beforeCapture fires each trigger's, first to last, and stops at the first failure.
func (t captureTriggers) beforeCapture(ctx context.Context) ([]string, error) {
	var killed []string
	for _, trigger := range t {
		triggerKilled, err := trigger.beforeCapture(ctx)
		if err != nil {
			return nil, err
		}
		killed = append(killed, triggerKilled...)
	}
	return killed, nil
}

// afterCapture fires every trigger's, last to first, whether its beforeCapture ran or not; the
// first failure is returned, the rest logged.
func (t captureTriggers) afterCapture(revertCtx context.Context) error {
	var firstErr error
	for _, trigger := range slices.Backward(t) {
		err := trigger.afterCapture(revertCtx)
		switch {
		case err == nil:
		case firstErr == nil:
			firstErr = err
		default:
			zerolog.Ctx(revertCtx).Error().Err(err).Msg("capture trigger failed after the capture")
		}
	}
	return firstErr
}

// stopTrigger stops the services before a capture and starts the whole project after it.
type stopTrigger struct {
	composeRunner composeRunner
	project       *types.Project
	waitTimeout   time.Duration
}

// beforeCapture puts the services down and returns the containers the stop deadline killed.
func (t stopTrigger) beforeCapture(ctx context.Context) ([]string, error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindComposeStop}, progress.MeasureResources)
	killed, err := t.composeRunner.Stop(ctx, t.project)
	step.End(err)
	if err != nil {
		return nil, fmt.Errorf("stopping services: %w", err)
	}
	return killed, nil
}

// afterCapture brings the services back and waits on their health checks.
func (t stopTrigger) afterCapture(revertCtx context.Context) error {
	ctx, step := progress.Run(revertCtx, progress.Step{Kind: kindComposeStart}, progress.MeasureResources)
	err := t.composeRunner.Start(ctx, t.project, t.waitTimeout)
	step.End(err)
	if err != nil {
		return fmt.Errorf("starting services: %w", err)
	}
	return nil
}
