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

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The warnings a trigger gives for each replica it resumes that it did not freeze.
const (
	warnResumedToStop       = "%s was paused: resuming it to stop it"
	warnResumedAfterCapture = "%s was paused before the capture: resuming it"
)

// captureTrigger is what fires on a group of services before a capture, and back after it.
// afterCapture must converge: it runs whether beforeCapture did or not.
type captureTrigger interface {
	beforeCapture(ctx context.Context) (killed []string, err error)
	afterCapture(revertCtx context.Context) error
}

// captureTriggers are the triggers of one capture, in the order beforeCapture fires them.
type captureTriggers []captureTrigger

// newCaptureTriggersByMode builds the triggers of plan: the stop, the pause and the live services,
// each group its own trigger.
func newCaptureTriggersByMode(composeRunner composeRunner, control containerControl, plan Plan) captureTriggers {
	stopped := plan.servicesHeldBy(model.CaptureModeStop)
	paused := plan.servicesHeldBy(model.CaptureModePause)
	live := plan.servicesHeldBy(model.CaptureModeLive)
	return captureTriggers{
		stopTrigger{
			composeRunner: composeRunner,
			control:       control,
			project:       plan.Project,
			waitTimeout:   plan.WaitTimeout,
			services:      newServiceNames(stopped),
			allServices:   len(stopped) == len(plan.Services),
			pausedOnEntry: newPausedOnEntry(stopped),
			resumed: slices.Concat(
				newRunningReplicas(paused),
				newPausedOnEntry(paused),
				newPausedOnEntry(live),
			),
		},
		pauseTrigger{
			control:       control,
			running:       newRunningReplicas(paused),
			pausedOnEntry: newPausedOnEntry(paused),
		},
		liveTrigger{
			control:       control,
			pausedOnEntry: newPausedOnEntry(live),
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

// stopTrigger stops the stop services before a capture and starts the whole project after it.
type stopTrigger struct {
	composeRunner composeRunner
	control       containerControl
	project       *types.Project
	waitTimeout   time.Duration
	// services are the ones to stop; allServices is set when they are every service of the plan.
	services    []string
	allServices bool
	// pausedOnEntry are their replicas found frozen, resumed so that compose can stop them.
	pausedOnEntry []model.ContainerRef
	// resumed are the replicas the other triggers resume after the capture, whose health the
	// start waits out first.
	resumed []model.ContainerRef
}

// beforeCapture resumes the frozen replicas, puts the services down and returns the containers
// the stop deadline killed.
func (t stopTrigger) beforeCapture(ctx context.Context) ([]string, error) {
	var (
		killed []string
		err    error
	)
	if err = unpauseReplicas(ctx, t.control, t.pausedOnEntry, t.pausedOnEntry, warnResumedToStop); err != nil {
		return nil, err
	}
	if len(t.services) == 0 {
		return nil, nil
	}

	if t.allServices {
		killed, err = t.composeRunner.Stop(ctx, t.project)
	} else {
		killed, err = t.composeRunner.StopServices(ctx, t.project, t.services)
	}
	if err != nil {
		return nil, fmt.Errorf("stopping services: %w", err)
	}
	return killed, nil
}

// afterCapture starts the whole project, not its own services alone: whatever was stopped — by
// this trigger or before the capture — runs after it, and a running container is left as it is.
// The resumed replicas' next probe is waited on first, compose refusing a dependency on the
// unhealthy a pause leaves; a failed wait is only logged.
func (t stopTrigger) afterCapture(revertCtx context.Context) error {
	if len(t.resumed) > 0 {
		if err := t.control.AwaitHealthProbes(revertCtx, newContainerIDs(t.resumed)); err != nil {
			zerolog.Ctx(revertCtx).Warn().Err(err).Msg("cannot await the resumed containers' health probes")
		}
	}
	if err := t.composeRunner.Start(revertCtx, t.project, t.waitTimeout); err != nil {
		return fmt.Errorf("starting services: %w", err)
	}
	return nil
}

// pauseTrigger freezes the running replicas of the pause services before a capture and resumes
// every frozen one of them after it.
type pauseTrigger struct {
	control       containerControl
	running       []model.ContainerRef
	pausedOnEntry []model.ContainerRef
}

// beforeCapture freezes the running replicas.
func (t pauseTrigger) beforeCapture(ctx context.Context) ([]string, error) {
	if len(t.running) == 0 {
		return nil, nil
	}
	if err := t.control.PauseContainers(ctx, newContainerIDs(t.running)); err != nil {
		return nil, fmt.Errorf("pausing containers: %w", err)
	}
	return nil, nil
}

// afterCapture resumes whichever of the replicas is frozen, the ones frozen before the capture
// with a warning; it converges whether beforeCapture ran, failed partway or never ran.
func (t pauseTrigger) afterCapture(revertCtx context.Context) error {
	replicas := slices.Concat(t.running, t.pausedOnEntry)
	return unpauseReplicas(revertCtx, t.control, replicas, t.pausedOnEntry, warnResumedAfterCapture)
}

// liveTrigger holds the live services by nothing, and resumes their replicas found frozen.
type liveTrigger struct {
	control       containerControl
	pausedOnEntry []model.ContainerRef
}

// beforeCapture leaves the services running.
func (liveTrigger) beforeCapture(context.Context) ([]string, error) {
	return nil, nil
}

// afterCapture resumes the replicas found frozen.
func (t liveTrigger) afterCapture(revertCtx context.Context) error {
	return unpauseReplicas(revertCtx, t.control, t.pausedOnEntry, t.pausedOnEntry, warnResumedAfterCapture)
}

// unpauseReplicas resumes whichever of replicas is frozen, warning on the step of ctx of each of
// foreign by format.
func unpauseReplicas(
	ctx context.Context, control containerControl, replicas, foreign []model.ContainerRef, format string,
) error {
	if len(replicas) == 0 {
		return nil
	}
	step := progress.Current(ctx)
	for _, ref := range foreign {
		step.Warn(fmt.Sprintf(format, newReplicaName(ref)))
	}
	if err := control.UnpauseContainers(ctx, newContainerIDs(replicas)); err != nil {
		return fmt.Errorf("resuming containers: %w", err)
	}
	return nil
}

// newServiceNames names the services.
func newServiceNames(services []ServiceHold) []string {
	var names []string
	for _, service := range services {
		names = append(names, service.Service)
	}
	return names
}

// newRunningReplicas are the services' running replicas.
func newRunningReplicas(services []ServiceHold) []model.ContainerRef {
	var running []model.ContainerRef
	for _, service := range services {
		running = append(running, service.Running...)
	}
	return running
}

// newPausedOnEntry are the services' replicas found frozen.
func newPausedOnEntry(services []ServiceHold) []model.ContainerRef {
	var paused []model.ContainerRef
	for _, service := range services {
		paused = append(paused, service.pausedOnEntry()...)
	}
	return paused
}

// newContainerIDs are the replicas' container ids.
func newContainerIDs(replicas []model.ContainerRef) []string {
	ids := make([]string, 0, len(replicas))
	for _, ref := range replicas {
		ids = append(ids, ref.ID)
	}
	return ids
}

// newReplicaName is how a warning names a replica: <service>-<replica>.
func newReplicaName(ref model.ContainerRef) string {
	return fmt.Sprintf("%s-%d", ref.Service, ref.Replica)
}
