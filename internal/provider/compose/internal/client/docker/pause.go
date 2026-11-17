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

package docker

import (
	"cmp"
	"context"
	"fmt"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"golang.org/x/sync/errgroup"
)

// The daemon's healthcheck defaults, for a container whose config names none.
const (
	defaultHealthInterval = 30 * time.Second
	defaultHealthTimeout  = 30 * time.Second
)

// staleHealthPoll is how often a resumed container's health is read until its next probe.
const staleHealthPoll = 200 * time.Millisecond

// PauseContainers freezes the containers, all at once; one that is not running fails.
func (e *Engine) PauseContainers(ctx context.Context, ids []string) error {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, id := range ids {
		eg.Go(func() error {
			if _, err := e.api.ContainerPause(egCtx, id, client.ContainerPauseOptions{}); err != nil {
				return fmt.Errorf("pause container %s: %w", id, err)
			}
			return nil
		})
	}
	return eg.Wait()
}

// UnpauseContainers thaws whichever of the containers is paused, all at once; one that is not
// paused, or is gone, is left alone. The state is read first: the daemon refuses an unpause of a
// container not paused with no code to tell it by. One that fails leaves the others their
// attempt; the first failure is returned.
func (e *Engine) UnpauseContainers(ctx context.Context, ids []string) error {
	// No context of the group's own: a failure must not cancel the thaw of the rest.
	var eg errgroup.Group
	for _, id := range ids {
		eg.Go(func() error { return e.unpauseContainer(ctx, id) })
	}
	return eg.Wait()
}

// AwaitHealthProbes waits, all at once, until none of the containers reads the unhealthy a pause
// leaves with no failed probe, each at most one probe's interval and timeout; past that the
// health is the caller's to judge. One reading no such health, or gone, is not waited on.
func (e *Engine) AwaitHealthProbes(ctx context.Context, ids []string) error {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, id := range ids {
		eg.Go(func() error { return e.awaitHealthProbe(egCtx, id) })
	}
	return eg.Wait()
}

// unpauseContainer thaws one container if it is paused.
func (e *Engine) unpauseContainer(ctx context.Context, id string) error {
	inspected, err := e.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("inspect container %s: %w", id, err)
	case inspected.Container.State == nil || !inspected.Container.State.Paused:
		return nil
	}
	_, err = e.api.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("unpause container %s: %w", id, err)
	}
	return nil
}

// awaitHealthProbe waits out one container's stale health, bounded by its healthcheck.
func (e *Engine) awaitHealthProbe(ctx context.Context, id string) error {
	inspected, err := e.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("inspect container %s: %w", id, err)
	case !isStaleHealth(inspected.Container.State):
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, newProbeBound(inspected.Container.Config))
	defer cancel()

	ticker := time.NewTicker(cmp.Or(e.healthPoll, staleHealthPoll))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-probeCtx.Done():
		}
		inspected, err = e.api.ContainerInspect(probeCtx, id, client.ContainerInspectOptions{})
		switch {
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case probeCtx.Err() != nil, cerrdefs.IsNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("inspect container %s: %w", id, err)
		case !isStaleHealth(inspected.Container.State):
			return nil
		}
	}
}

// newProbeBound is one probe's interval and timeout, the daemon's defaults where config names none.
func newProbeBound(config *container.Config) time.Duration {
	var healthcheck container.HealthConfig
	if config != nil && config.Healthcheck != nil {
		healthcheck = *config.Healthcheck
	}
	return cmp.Or(healthcheck.Interval, defaultHealthInterval) + cmp.Or(healthcheck.Timeout, defaultHealthTimeout)
}

// isStaleHealth reports the unhealthy a pause leaves behind: no probe has failed since.
func isStaleHealth(state *container.State) bool {
	if state == nil || state.Health == nil {
		return false
	}
	return state.Health.Status == container.Unhealthy && state.Health.FailingStreak == 0
}
