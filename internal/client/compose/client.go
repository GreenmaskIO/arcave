package compose

import (
	"context"
	"fmt"
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
	defaultUpWaitTimeout = 120 * time.Second
	defaultStopTimeout   = 10 * time.Second
)

// dockerComposeAPI is the slice of the compose library's API that Client
// calls — consumer-side, so a test can assert on the options arcave passes without a
// daemon. api.Compose satisfies it.
type dockerComposeAPI interface {
	Stop(ctx context.Context, projectName string, options api.StopOptions) error
	Up(ctx context.Context, project *types.Project, options api.UpOptions) error
}

type dockerComposeLoader interface {
	Load(ctx context.Context, files ...string) (*types.Project, error)
}

// Client drives the *running* stack's lifecycle: it stops and starts the
// project's services, mirroring how the user would invoke `docker compose` by
// hand. Compose is where the Inspector's declared state becomes containers, and
// this is the type that moves it between the two — arcave stops the services before
// touching a mount and starts them after, and it is Client that does both.
//
// It calls github.com/docker/compose/v2 as a library rather than shelling out to
// the compose plugin, so arcave inherits compose's own semantics (depends_on
// ordering, health aggregation, container recreation) without requiring a docker
// CLI on the host — which matters for arcave mcp, where the environment may offer
// nothing but the daemon socket. The semantics are pinned to the compose version
// arcave was built against, not to whatever the user happens to have installed.
//
// The compose files to act on are passed per call, so a single instance is
// shared across invocations rather than bound to a fixed file set. The two
// timeouts are not: they are project config, resolved once at the composition
// root, so they are held rather than threaded through every call site.
type Client struct {
	api    dockerComposeAPI
	loader dockerComposeLoader
	opts   Options
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

// NewClient returns a runner over the engine's daemon connection, so the
// lifecycle half of this package targets the daemon resolved for the I/O half by
// construction. opts carries the project's bounds; a zero Options is valid and
// means "this package's defaults".
func NewClient(svc dockerComposeAPI, loader dockerComposeLoader, opts Options) *Client {
	return &Client{api: svc, loader: loader, opts: opts}
}

// Stop stops all project services. It returns once the containers have exited,
// so no extra polling is needed.
//
// Each container gets the configured grace period to exit on its own before
// compose SIGKILLs it. That bound is arcave's business in a way it is not the
// compose CLI's: the volumes are snapshotted the instant the containers are
// down, so a database that was still flushing when it was killed is what the
// savepoint captures.
func (c *Client) Stop(ctx context.Context, files ...string) error {
	project, err := c.load(ctx, files...)
	if err != nil {
		return err
	}
	timeout := c.stopTimeout()
	// Str, not Dur: zerolog renders a Dur in milliseconds, and "10000" is not what
	// a user reading a grace period wants to see.
	zerolog.Ctx(ctx).Debug().Str("stop_timeout", timeout.String()).Msg("stopping compose services")
	return c.api.Stop(ctx, project.Name, api.StopOptions{Project: project, Timeout: &timeout})
}

// Up starts all project services in the background and waits until they are
// healthy, honouring container health checks where defined.
//
// waitTimeout bounds how long it blocks on health checks: without a bound, a
// service that never becomes healthy (e.g. a crash-looping container with
// restart: always) would make Up hang forever, so the bound turns that into an
// error callers can act on (e.g. rollback reverts to its safety backup). A
// value <= 0 falls back to the configured Options.
//
// The effective bound is logged before compose is called, because this wait is
// where a arcave run spends minutes without finishing: the log line is what tells
// a user staring at a quiet terminal that arcave is waiting rather than wedged, and
// names the deadline it will fail at. Compose's own per-container progress
// arrives alongside it as further log records (see ProgressLogger), unless the
// project asked for compose.progress: tty or quiet.
func (c *Client) Up(ctx context.Context, waitTimeout time.Duration, files ...string) error {
	project, err := c.load(ctx, files...)
	if err != nil {
		return err
	}
	waitTimeout = c.waitTimeoutOr(waitTimeout)
	stopTimeout := c.stopTimeout()
	zerolog.Ctx(ctx).Info().Str("wait_timeout", waitTimeout.String()).
		Msg("waiting for services to become healthy")

	return c.api.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{
			Services: project.ServiceNames(),
			// Inherit carries a recreated container's anonymous volumes over from
			// the one it replaces. The compose CLI defaults it on (it is what
			// `--renew-anon-volumes` turns *off*), and the library does not — the
			// zero CreateOptions silently means "renew". arcave captures anonymous
			// volumes like any other mount, so leaving it off would throw their
			// data away whenever a container diverges from the compose file, which
			// is exactly what arcave's users are in the middle of doing.
			Inherit: true,
			// The CLI's defaults, spelled out: replace only containers whose
			// config no longer matches the file. The zero value happens to behave
			// the same way, but naming it keeps that from being an accident.
			Recreate:             api.RecreateDiverged,
			RecreateDependencies: api.RecreateDiverged,
			// Recreating a container stops the one it replaces, so the same grace
			// period applies here as in Stop — otherwise a service arcave is careful
			// to shut down cleanly before a snapshot gets SIGKILLed on the way up.
			Timeout: &stopTimeout,
		},
		Start: api.StartOptions{
			Project:     project,
			Services:    project.ServiceNames(),
			Wait:        true,
			WaitTimeout: waitTimeout,
		},
	})
}

// waitTimeoutOr resolves the bound a call named against the configured one: a
// caller that names none (an MCP tool call omitting wait_timeout) gets the
// project's, and a Client built with a zero Options still gets a bound rather
// than an unbounded wait.
func (c *Client) waitTimeoutOr(waitTimeout time.Duration) time.Duration {
	if waitTimeout <= 0 {
		waitTimeout = c.opts.WaitTimeout
	}
	if waitTimeout <= 0 {
		waitTimeout = defaultUpWaitTimeout
	}
	return waitTimeout
}

// stopTimeout is the configured grace period, or this package's default when the
// Client was built without one. No call names its own: nothing above has ever
// needed to stop one workflow harder than another.
func (c *Client) stopTimeout() time.Duration {
	if c.opts.StopTimeout <= 0 {
		return defaultStopTimeout
	}
	return c.opts.StopTimeout
}

// load resolves the project the same way Inspector does, then labels it.
func (c *Client) load(ctx context.Context, files ...string) (*types.Project, error) {
	project, err := c.loader.Load(ctx, files...)
	if err != nil {
		return nil, fmt.Errorf("loading docker compose file(s): %w", err)
	}
	return labelProject(project), nil
}

// labelProject stamps each service with the labels compose puts on the
// containers it creates. The compose CLI does this before every call, and the
// library does not do it for you: without them compose does not recognise its
// own containers, so an Up would recreate a stack it should have reused and a
// Stop would find nothing to stop.
func labelProject(project *types.Project) *types.Project {
	for name, service := range project.Services {
		service.CustomLabels = map[string]string{
			api.ProjectLabel:     project.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  project.WorkingDir,
			api.ConfigFilesLabel: strings.Join(project.ComposeFiles, ","),
			api.OneoffLabel:      "False",
		}
		project.Services[name] = service
	}
	return project
}
