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

// Package hold holds a compose project still for a capture, each service by its capture mode,
// and brings it back after.
package hold

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/captureplan"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The warnings a trigger gives for each replica it resumes that it did not freeze.
const (
	warnResumedToStop       = "%s was paused: resuming it to stop it"
	warnResumedAfterCapture = "%s was paused before the capture: resuming it"
)

// ComposeRunner stops the stop services for a capture and starts the compose project after.
type ComposeRunner interface {
	// Stop stops every service of project and returns the ids of the containers its deadline
	// killed.
	Stop(ctx context.Context, project *types.Project) ([]string, error)
	// StopServices stops the named services alone, and returns the same.
	StopServices(ctx context.Context, project *types.Project, services []string) ([]string, error)
	// Start starts every service of project and waits on their health checks; a running
	// container is left as it is.
	Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error
}

// ContainerControl freezes the pause services' replicas for a capture and resumes them after.
type ContainerControl interface {
	PauseContainers(ctx context.Context, ids []string) error
	// UnpauseContainers resumes whichever of the containers is paused; one that is not, or is
	// gone, is no error.
	UnpauseContainers(ctx context.Context, ids []string) error
	// AwaitHealthProbes waits out the unhealthy a pause leaves on the containers until their
	// next probe.
	AwaitHealthProbes(ctx context.Context, ids []string) error
}

// trigger is what fires on a group of services before a capture, and back after it.
// afterCapture must converge: it runs whether beforeCapture did or not.
type trigger interface {
	beforeCapture(ctx context.Context) (killed []string, err error)
	afterCapture(revertCtx context.Context) error
}

// Triggers are the triggers of one capture, in the order beforeCapture fires them.
type Triggers []trigger

// NewTriggersByMode builds the triggers of plan: the stop, the pause and the live services,
// each group its own trigger; the start after the capture waits waitTimeout on health checks.
func NewTriggersByMode(
	composeRunner ComposeRunner, control ContainerControl, plan captureplan.Plan, waitTimeout time.Duration,
) Triggers {
	stopped := plan.ServicesHeldBy(model.CaptureModeStop)
	paused := plan.ServicesHeldBy(model.CaptureModePause)
	live := plan.ServicesHeldBy(model.CaptureModeLive)
	return Triggers{
		stopTrigger{
			composeRunner: composeRunner,
			control:       control,
			project:       plan.Project,
			waitTimeout:   waitTimeout,
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

// BeforeCapture fires each trigger's, first to last, and stops at the first failure.
func (t Triggers) BeforeCapture(ctx context.Context) ([]string, error) {
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

// AfterCapture fires every trigger's, last to first, whether its beforeCapture ran or not; the
// first failure is returned, the rest logged.
func (t Triggers) AfterCapture(revertCtx context.Context) error {
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
	composeRunner ComposeRunner
	control       ContainerControl
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
	control       ContainerControl
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
	control       ContainerControl
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
	ctx context.Context, control ContainerControl, replicas, foreign []model.ContainerRef, format string,
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
func newServiceNames(services []captureplan.ServiceHold) []string {
	var names []string
	for _, service := range services {
		names = append(names, service.Service)
	}
	return names
}

// newRunningReplicas are the services' running replicas.
func newRunningReplicas(services []captureplan.ServiceHold) []model.ContainerRef {
	var running []model.ContainerRef
	for _, service := range services {
		running = append(running, service.Running...)
	}
	return running
}

// newPausedOnEntry are the services' replicas found frozen.
func newPausedOnEntry(services []captureplan.ServiceHold) []model.ContainerRef {
	var paused []model.ContainerRef
	for _, service := range services {
		paused = append(paused, service.PausedOnEntry()...)
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
