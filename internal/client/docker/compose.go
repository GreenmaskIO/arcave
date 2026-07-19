package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/compose/v2/pkg/compose"

	composeclient "github.com/greenmaskio/gm-tools/internal/client/compose"
)

// DefaultUpWaitTimeout is the bound (seconds) Up applies to its health-check
// wait when the caller names none. Without a bound, a service that never becomes
// healthy (a crash-looping container with restart: always) would block forever.
//
// It is the default of *this* implementation, not a property of the domain: a
// runner that did not wait on health checks would have no use for it. Callers
// that expose it — the command layer, for a flag default — take it from here.
const DefaultUpWaitTimeout = 120

// composeService is the slice of the compose library's API that ComposeRunner
// calls — consumer-side, so a test can assert on the options GM passes without a
// daemon. api.Compose satisfies it.
type composeService interface {
	Stop(ctx context.Context, projectName string, options api.StopOptions) error
	Up(ctx context.Context, project *types.Project, options api.UpOptions) error
}

// ComposeRunner drives the *running* stack's lifecycle: it stops and starts the
// project's services, mirroring how the user would invoke `docker compose` by
// hand. Compose is where the Inspector's declared state becomes containers, and
// this is the type that moves it between the two — GM stops the services before
// touching a mount and starts them after, and it is ComposeRunner that does both.
//
// It calls github.com/docker/compose/v2 as a library rather than shelling out to
// the compose plugin, so GM inherits compose's own semantics (depends_on
// ordering, health aggregation, container recreation) without requiring a docker
// CLI on the host — which matters for gm mcp, where the environment may offer
// nothing but the daemon socket. The semantics are pinned to the compose version
// GM was built against, not to whatever the user happens to have installed.
//
// It is stateless: the compose files to act on are passed per call, so a single
// instance is shared across invocations rather than bound to a fixed file set.
type ComposeRunner struct {
	svc composeService
}

// NewComposeRunner returns a runner over the engine's daemon connection, so the
// lifecycle half of this package targets the daemon Options resolved for the I/O
// half by construction.
func NewComposeRunner(e *Engine) *ComposeRunner {
	return &ComposeRunner{svc: compose.NewComposeService(e.dockerCli)}
}

// Stop stops all project services. It returns once the containers have exited,
// so no extra polling is needed.
func (c *ComposeRunner) Stop(ctx context.Context, files ...string) error {
	project, err := c.load(ctx, files...)
	if err != nil {
		return err
	}
	return c.svc.Stop(ctx, project.Name, api.StopOptions{Project: project})
}

// Up starts all project services in the background and waits until they are
// healthy, honouring container health checks where defined.
//
// waitTimeoutSec bounds how long it blocks on health checks: without a bound, a
// service that never becomes healthy (e.g. a crash-looping container with
// restart: always) would make Up hang forever, so the bound turns that into an
// error callers can act on (e.g. rollback reverts to its safety backup). A
// value <= 0 falls back to DefaultUpWaitTimeout.
func (c *ComposeRunner) Up(ctx context.Context, waitTimeoutSec int, files ...string) error {
	if waitTimeoutSec <= 0 {
		waitTimeoutSec = DefaultUpWaitTimeout
	}
	project, err := c.load(ctx, files...)
	if err != nil {
		return err
	}
	return c.svc.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{
			Services: project.ServiceNames(),
			// Inherit carries a recreated container's anonymous volumes over from
			// the one it replaces. The compose CLI defaults it on (it is what
			// `--renew-anon-volumes` turns *off*), and the library does not — the
			// zero CreateOptions silently means "renew". GM captures anonymous
			// volumes like any other mount, so leaving it off would throw their
			// data away whenever a container diverges from the compose file, which
			// is exactly what GM's users are in the middle of doing.
			Inherit: true,
			// The CLI's defaults, spelled out: replace only containers whose
			// config no longer matches the file. The zero value happens to behave
			// the same way, but naming it keeps that from being an accident.
			Recreate:             api.RecreateDiverged,
			RecreateDependencies: api.RecreateDiverged,
		},
		Start: api.StartOptions{
			Project:     project,
			Services:    project.ServiceNames(),
			Wait:        true,
			WaitTimeout: time.Duration(waitTimeoutSec) * time.Second,
		},
	})
}

// load resolves the project the same way Inspector does, then labels it.
func (c *ComposeRunner) load(ctx context.Context, files ...string) (*types.Project, error) {
	project, err := composeclient.Load(ctx, files...)
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
