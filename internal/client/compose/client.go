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

package compose

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/rs/zerolog"
)

// The bounds Client falls back to when it is handed no Options at all — a direct
// caller (a test, an embedder) rather than arcave's own composition root, which
// resolves both from config.Compose. They are last resorts, not the product's
// defaults: config.DefaultWaitTimeout and config.DefaultStopTimeout are what a
// user sees and overrides, and these exist so a zero Options can never mean
// "wait forever" or "SIGKILL immediately".
const (
	defaultStartWaitTimeout = 120 * time.Second
	defaultStopTimeout      = 10 * time.Second
)

// sigkillExitCode is the exit code of a container SIGKILLed: 128 + 9.
const sigkillExitCode = 137

// waitPollInterval is how often Start re-reads the containers it waits for; compose's own.
const waitPollInterval = 500 * time.Millisecond

// dockerComposeAPI is the slice of the compose library's API that Client calls.
// api.Compose satisfies it.
type dockerComposeAPI interface {
	Stop(ctx context.Context, projectName string, options api.StopOptions) error
	Down(ctx context.Context, projectName string, options api.DownOptions) error
	Create(ctx context.Context, project *types.Project, options api.CreateOptions) error
	Start(ctx context.Context, projectName string, options api.StartOptions) error
	Ps(ctx context.Context, projectName string, options api.PsOptions) ([]api.ContainerSummary, error)
}

// Client runs the project's lifecycle, the compose project to act on passed per call. It
// calls compose/v2 as a library, so the host needs no docker CLI.
type Client struct {
	api          dockerComposeAPI
	opts         Options
	pollInterval time.Duration
}

// Options are the project's compose-lifecycle bounds. They are durations, not a
// count of seconds: the unit is chosen where the user writes it (config.Duration
// parses `2m`), so no layer below that one has to remember which one it is
// holding. A zero field falls back to this package's own default.
type Options struct {
	// WaitTimeout bounds Up's health-check wait when a call names no bound of its
	// own.
	WaitTimeout time.Duration
	// StopTimeout is the least grace period before Stop and Down SIGKILL a container;
	// a service's stop_grace_period raises it.
	StopTimeout time.Duration
}

// NewClient returns a runner over the engine's daemon connection. opts carries the
// project's bounds; a zero Options means this package's defaults.
func NewClient(svc dockerComposeAPI, opts Options) *Client {
	return &Client{api: svc, opts: opts, pollInterval: waitPollInterval}
}

// Stop stops all project services and returns once the containers have exited,
// warning of any the stop timeout ran out on and SIGKILLed.
func (c *Client) Stop(ctx context.Context, project *types.Project) error {
	timeout := c.logStopTimeout(ctx, project, "stopping compose services")
	running := c.runningContainers(ctx, project)

	if err := c.api.Stop(ctx, project.Name, api.StopOptions{Project: project, Timeout: &timeout}); err != nil {
		return fmt.Errorf("stop compose services: %w", err)
	}
	c.warnKilled(ctx, project, running, timeout)
	return nil
}

// Down removes the project's containers and networks. Volumes are kept: a volume
// arcave does not capture has no copy anywhere else.
func (c *Client) Down(ctx context.Context, project *types.Project) error {
	timeout := c.logStopTimeout(ctx, project, "removing compose containers and networks")

	return c.api.Down(ctx, project.Name, api.DownOptions{
		Project:       project,
		Timeout:       &timeout,
		RemoveOrphans: true,
		Volumes:       false,
	})
}

// Create builds the project's containers, networks and volumes without starting
// them. Inherit is off so an anonymous volume is created fresh, not reused.
func (c *Client) Create(ctx context.Context, project *types.Project) error {
	timeout := c.stopTimeout()
	zerolog.Ctx(ctx).Info().Msg("creating compose containers")

	return c.api.Create(ctx, withComposeLabels(project), api.CreateOptions{
		Timeout:              &timeout,
		Recreate:             api.RecreateNever,
		RecreateDependencies: api.RecreateNever,
		Inherit:              false,
		RemoveOrphans:        true,
		QuietPull:            true,
		AssumeYes:            true,
	})
}

// Start starts all project services and waits until they are healthy; a waitTimeout <= 0
// falls back to Options. Start rather than Up: what Create built must not be recreated under it.
// A project with a disabled healthcheck is waited for by Start itself: compose refuses one.
func (c *Client) Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	waitTimeout = c.waitTimeoutOr(waitTimeout)
	zerolog.Ctx(ctx).Info().Str("wait_timeout", waitTimeout.String()).
		Msg("waiting for services to become healthy")

	if !healthCheckDisabled(project) {
		return c.api.Start(ctx, project.Name, api.StartOptions{
			Project:     project,
			Wait:        true,
			WaitTimeout: waitTimeout,
		})
	}

	// WaitTimeout still bounds the depends_on waits compose runs while it starts.
	if err := c.api.Start(ctx, project.Name, api.StartOptions{Project: project, WaitTimeout: waitTimeout}); err != nil {
		return err
	}
	return c.waitReady(ctx, project, waitTimeout)
}

// logStopTimeout resolves the stop bound for project and logs it with msg.
func (c *Client) logStopTimeout(ctx context.Context, project *types.Project, msg string) time.Duration {
	timeout, raisedBy := c.stopTimeoutFor(project)
	// Str, not Dur: zerolog renders a Dur in milliseconds, and "10000" is not what
	// a user reading a grace period wants to see.
	event := zerolog.Ctx(ctx).Info().Str("stop_timeout", timeout.String())
	if raisedBy != "" {
		event = event.Str("raised_by", raisedBy)
	}
	event.Msg(msg)
	return timeout
}

// stopTimeoutFor is the configured grace period raised to the longest stop_grace_period
// the project declares, and the service that raised it, if one did. It is rounded up to
// whole seconds: compose truncates it to them, and 500ms would be no grace at all.
func (c *Client) stopTimeoutFor(project *types.Project) (time.Duration, string) {
	timeout, raisedBy := c.stopTimeout(), ""
	for _, name := range slices.Sorted(maps.Keys(project.Services)) {
		grace := project.Services[name].StopGracePeriod
		if grace != nil && time.Duration(*grace) > timeout {
			timeout, raisedBy = time.Duration(*grace), name
		}
	}
	return (timeout + time.Second - 1).Truncate(time.Second), raisedBy
}

// stopTimeout is the configured grace period, or this package's default when the
// Client was built without one.
func (c *Client) stopTimeout() time.Duration {
	if c.opts.StopTimeout <= 0 {
		return defaultStopTimeout
	}
	return c.opts.StopTimeout
}

// runningContainers are the ids of the project's running containers; a failed read
// is logged and yields none, so nothing is warned of.
func (c *Client) runningContainers(ctx context.Context, project *types.Project) map[string]bool {
	containers, err := c.api.Ps(ctx, project.Name, api.PsOptions{Project: project})
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("cannot list running containers; a killed one will not be reported")
		return nil
	}
	running := make(map[string]bool, len(containers))
	for _, ctr := range containers {
		running[ctr.ID] = true
	}
	return running
}

// warnKilled warns of every container that was running and exited 137: the stop hit
// its deadline and SIGKILLed it, so its volumes may hold a crash state.
func (c *Client) warnKilled(ctx context.Context, project *types.Project, running map[string]bool, timeout time.Duration) {
	if len(running) == 0 {
		return
	}
	logger := zerolog.Ctx(ctx)
	containers, err := c.api.Ps(ctx, project.Name, api.PsOptions{Project: project, All: true})
	if err != nil {
		logger.Warn().Err(err).Msg("cannot list stopped containers; a killed one will not be reported")
		return
	}
	for _, ctr := range containers {
		if !running[ctr.ID] || ctr.State != container.StateExited || ctr.ExitCode != sigkillExitCode {
			continue
		}
		if service, ok := project.Services[ctr.Service]; ok && stopsWithSIGKILL(service.StopSignal) {
			continue
		}
		logger.Warn().Str("container", ctr.Name).Str("stop_timeout", timeout.String()).
			Msgf("container %s did not exit within %s and was killed; its volumes may hold a crash state — "+
				"raise compose.stop_timeout or the service's stop_grace_period", ctr.Name, timeout)
	}
}

// stopsWithSIGKILL reports a stop_signal whose normal exit is 137.
func stopsWithSIGKILL(signal string) bool {
	switch strings.TrimPrefix(strings.ToUpper(signal), "SIG") {
	case "KILL", "9":
		return true
	}
	return false
}

// withComposeLabels stamps onto a copy of the project the labels compose puts on
// every container it creates: the library leaves that to its CLI, and without
// them nothing finds the compose project again.
func withComposeLabels(project *types.Project) *types.Project {
	stamped := *project
	stamped.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		service.CustomLabels = types.Labels{
			api.ProjectLabel:     project.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  project.WorkingDir,
			api.ConfigFilesLabel: strings.Join(project.ComposeFiles, ","),
			api.OneoffLabel:      "False",
		}
		stamped.Services[name] = service
	}
	return &stamped
}

// healthCheckDisabled reports a service compose creates with a healthcheck of NONE: its
// start --wait refuses one, having no health to read.
func healthCheckDisabled(project *types.Project) bool {
	for _, service := range project.Services {
		check := service.HealthCheck
		if check != nil && (check.Disable || (len(check.Test) > 0 && check.Test[0] == "NONE")) {
			return true
		}
	}
	return false
}

// waitReady waits as compose's start --wait does: healthy where a health is reported, running
// where none is. A one-shot others wait to complete is skipped; Start has already waited for it.
func (c *Client) waitReady(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()

	oneShots := completionAwaited(project)
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		ready, err := c.containersReady(ctx, project, oneShots)
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return fmt.Errorf("application not healthy after %s", waitTimeout)
		case err != nil:
			return err
		case ready:
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		}
	}
}

// completionAwaited are the services another depends on with service_completed_successfully.
func completionAwaited(project *types.Project) map[string]bool {
	oneShots := map[string]bool{}
	for _, dependent := range project.Services {
		for name, dependency := range dependent.DependsOn {
			if dependency.Condition == types.ServiceConditionCompletedSuccessfully {
				oneShots[name] = true
			}
		}
	}
	return oneShots
}

// containersReady reads the project's containers once: ready when every one runs and none
// reports a health short of healthy; an exited or unhealthy one fails.
func (c *Client) containersReady(ctx context.Context, project *types.Project, oneShots map[string]bool) (bool, error) {
	containers, err := c.api.Ps(ctx, project.Name, api.PsOptions{Project: project, All: true})
	if err != nil {
		return false, err
	}

	ready := true
	for _, ctr := range containers {
		if oneShots[ctr.Service] {
			continue
		}
		switch ctr.State {
		case string(container.StateExited), string(container.StateDead):
			return false, fmt.Errorf("container %s exited (%d)", ctr.Name, ctr.ExitCode)
		case string(container.StateRunning):
		default:
			ready = false
			continue
		}
		switch ctr.Health {
		case "", container.Healthy:
		case container.Unhealthy:
			return false, fmt.Errorf("container %s is unhealthy", ctr.Name)
		default:
			ready = false
		}
	}
	return ready, nil
}

// waitTimeoutOr resolves the bound a call named against the configured one, falling
// back to this package's default so no wait is ever unbounded.
func (c *Client) waitTimeoutOr(waitTimeout time.Duration) time.Duration {
	if waitTimeout <= 0 {
		waitTimeout = c.opts.WaitTimeout
	}
	if waitTimeout <= 0 {
		waitTimeout = defaultStartWaitTimeout
	}
	return waitTimeout
}
