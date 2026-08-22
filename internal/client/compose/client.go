package compose

import (
	"context"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
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

// dockerComposeAPI is the slice of the compose library's API that Client calls.
// api.Compose satisfies it.
type dockerComposeAPI interface {
	Stop(ctx context.Context, projectName string, options api.StopOptions) error
	Down(ctx context.Context, projectName string, options api.DownOptions) error
	Create(ctx context.Context, project *types.Project, options api.CreateOptions) error
	Start(ctx context.Context, projectName string, options api.StartOptions) error
}

// Client runs the project's lifecycle, the stack to act on passed per call. It
// calls compose/v2 as a library, so the host needs no docker CLI.
type Client struct {
	api  dockerComposeAPI
	opts Options
}

// Options are the project's compose-lifecycle bounds. They are durations, not a
// count of seconds: the unit is chosen where the user writes it (config.Duration
// parses `2m`), so no layer below that one has to remember which one it is
// holding. A zero field falls back to this package's own default.
type Options struct {
	// WaitTimeout bounds Up's health-check wait when a call names no bound of its
	// own.
	WaitTimeout time.Duration
	// StopTimeout is the grace period a container gets to exit before compose
	// SIGKILLs it. It applies to Stop, and to the stop half of any container Up
	// recreates.
	StopTimeout time.Duration
}

// NewClient returns a runner over the engine's daemon connection. opts carries the
// project's bounds; a zero Options means this package's defaults.
func NewClient(svc dockerComposeAPI, opts Options) *Client {
	return &Client{api: svc, opts: opts}
}

// Stop stops all project services and returns once the containers have exited.
// Each gets the configured grace period to exit on its own before a SIGKILL.
func (c *Client) Stop(ctx context.Context, project *types.Project) error {
	timeout := c.stopTimeout()
	// Str, not Dur: zerolog renders a Dur in milliseconds, and "10000" is not what
	// a user reading a grace period wants to see.
	zerolog.Ctx(ctx).Debug().Str("stop_timeout", timeout.String()).Msg("stopping compose services")
	return c.api.Stop(ctx, project.Name, api.StopOptions{Project: project, Timeout: &timeout})
}

// Down removes the project's containers and networks. Volumes are kept: a volume
// arcave does not capture has no copy anywhere else.
func (c *Client) Down(ctx context.Context, project *types.Project) error {
	timeout := c.stopTimeout()
	zerolog.Ctx(ctx).Info().Str("stop_timeout", timeout.String()).Msg("removing compose containers and networks")

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
func (c *Client) Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	waitTimeout = c.waitTimeoutOr(waitTimeout)
	zerolog.Ctx(ctx).Info().Str("wait_timeout", waitTimeout.String()).
		Msg("waiting for services to become healthy")

	return c.api.Start(ctx, project.Name, api.StartOptions{
		Project:     project,
		Wait:        true,
		WaitTimeout: waitTimeout,
	})
}

// withComposeLabels stamps onto a copy of the project the labels compose puts on
// every container it creates: the library leaves that to its CLI, and without
// them nothing finds the stack again.
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

// stopTimeout is the configured grace period, or this package's default when the
// Client was built without one.
func (c *Client) stopTimeout() time.Duration {
	if c.opts.StopTimeout <= 0 {
		return defaultStopTimeout
	}
	return c.opts.StopTimeout
}
