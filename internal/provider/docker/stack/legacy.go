package stack

import (
	"context"
	"fmt"
	"os"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/core"
	sprepo "github.com/greenmaskio/arcave/internal/provider/docker/internal/repository/savepoint"
	arcavecompose "github.com/greenmaskio/arcave/internal/provider/docker/stack/internal/client/compose"
	introspect "github.com/greenmaskio/arcave/internal/provider/docker/stack/internal/introspect"
	arcrepo "github.com/greenmaskio/arcave/internal/provider/docker/stack/internal/repository/arc"
	cfgrepo "github.com/greenmaskio/arcave/internal/repository/config"
	"github.com/greenmaskio/arcave/internal/service/printer/json"
)

// legacyEngine wires the savepoint service. Its order is load-bearing: stack,
// store, restic server, backends, then the service.
func (p *Provider) legacyEngine(
	ctx context.Context,
	projectRoot string,
	project model.ProjectName,
	session model.SessionID,
	composeFiles ...string,
) (*core.Service, *legacyResources, error) {
	var opened partial
	defer func() { opened.releaseOnFailure(ctx) }()

	// The loader comes first: the store root ends at the compose project's name.
	// The compose runner below reuses it, so both resolve the same file set.
	loader := arcavecompose.NewLoader()
	composeProject, err := loader.Load(ctx, composeFiles...)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve compose project (its name is the stack segment of the store root): %w", err)
	}

	// One store for the whole stack, rooted inside the session.
	stackStore, err := p.newStackStore(projectRoot, project, session, model.ResourceName(composeProject.Name))
	if err != nil {
		return nil, nil, err
	}
	opened.add("stack store", stackStore)

	composeRunner := arcavecompose.NewClient(p.composeService, loader, arcavecompose.Options{
		WaitTimeout: p.settings.Compose.WaitTimeout,
		StopTimeout: p.settings.Compose.StopTimeout,
	})

	// The server cdc's containers fetch their repositories over: its port must be
	// known before the first backend is built.
	endpoint, err := p.startResticServer(ctx, stackStore)
	if err != nil {
		return nil, nil, err
	}
	opened.add("restic server", endpoint.server)

	backendRegistry, err := p.newBackendRegistry(endpoint)
	if err != nil {
		return nil, nil, err
	}

	// One repository over the one store, so every record travels with its data.
	svc := core.New(core.Deps{
		SavepointRepo:    sprepo.New(stackStore),
		ProjectLocker:    noopLocker{},
		ComposeInspector: introspect.NewInspector(loader),
		ComposeRunner:    composeRunner,
		BackendRegistry:  backendRegistry,
		Inflight:         p.newInflightMarker(projectRoot),
		Store:            stackStore,
		DefaultBackend:   p.settings.Snapshot.BackendKind(),
	})
	opened.keep()
	return svc, &legacyResources{store: stackStore, restic: endpoint}, nil
}

// legacyResources is what one legacyEngine call opened: the restic server is
// released first, so a container reports a shutdown as a shutdown.
type legacyResources struct {
	store  storages.Storager
	restic *resticEndpoint
}

func (r *legacyResources) Close(ctx context.Context) error {
	shutdownResticServer(ctx, r.restic.server)
	return r.store.Close()
}

// noopLocker stands in for the project flock: it warns rather than pretending to lock.
type noopLocker struct{}

func (noopLocker) Acquire(ctx context.Context) (func() error, error) {
	log.Ctx(ctx).Warn().Msg("project lock is not implemented: this operation is not serialised against any other")
	return func() error { return nil }, nil
}

// initServiceDeps wires `arcave init`: a config to write and a project to verify.
func initServiceDeps(projectPath string) (*core.InitService, *json.Printer) {
	svc := core.NewInitService(projectPath, cfgrepo.New(projectPath), introspect.NewInspector(arcavecompose.NewLoader()))
	return svc, json.New(os.Stdout)
}

// recommendServiceDeps wires `arcave recommend`. recommenderFiles are the
// optional user catalogs merged over the built-in one, in order.
func recommendServiceDeps(recommenderFiles []string) (*core.RecommendService, *json.Printer) {
	return newRecommendService(recommenderFiles), json.New(os.Stdout)
}

// newRecommendService builds the service without a printer: `arcave mcp` must
// not print, its stdout being the JSON-RPC channel.
func newRecommendService(recommenderFiles []string) *core.RecommendService {
	return core.NewRecommendService(arcrepo.NewARC(recommenderFiles...), introspect.NewInspector(arcavecompose.NewLoader()))
}
