package stack

import (
	"context"
	"fmt"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/rest/repo"
)

// resticShutdownTimeout bounds the restic server's graceful shutdown.
const resticShutdownTimeout = 5 * time.Second

// resticEndpoint is a listening restic server and the repository root it serves.
type resticEndpoint struct {
	server *echo.Echo
	root   storages.Storager
	// urlBase is the container's own name for the server joined to the port it bound.
	urlBase string
}

// startResticServer opens the repository root under stackStore and serves it.
// An address that will not bind fails here, before the first backend is built.
func (p *Provider) startResticServer(ctx context.Context, stackStore storages.Storager) (*resticEndpoint, error) {
	cdcCfg := p.settings.Backends.CDC

	// The tree every cdc repository lives in, one directory per volume.
	root, err := stackStore.SubStorage(cdc.RepoNamespace, true)
	if err != nil {
		return nil, fmt.Errorf("initialise restic repository root: %w", err)
	}

	server := rest.NewEcho(ctx, repo.NewResolver(root))
	port, err := rest.Serve(server, cdcCfg.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("start the restic server: %w", err)
	}
	return &resticEndpoint{
		server:  server,
		root:    root,
		urlBase: rest.RepositoryURLBase(cdcCfg.ServerScheme, cdcCfg.ContainerHost, port),
	}, nil
}

// shutdownResticServer stops serving gracefully: restic reports a severed
// connection as a repository error. The deadline is detached from ctx.
func shutdownResticServer(ctx context.Context, e *echo.Echo) {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resticShutdownTimeout)
	defer cancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("shut down restic server")
	}
}
