package stack

import (
	"fmt"

	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/core"
	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// newBackendRegistry is where a volume's Backend becomes an instance. cdc is
// wired whatever snapshot.backend says: a rollback obeys the record.
func (p *Provider) newBackendRegistry(endpoint *resticEndpoint) (dockermodel.SnapshotBackendRegistry, error) {
	registry, err := core.NewSnapshotBackendRegistry(
		fullcopy.NewFactory(p.engine),
		cdc.NewFactory(cdc.Dependencies{
			Runner:         p.engine,
			RepoURLBase:    endpoint.urlBase,
			RepositoryRoot: endpoint.root,
			Options:        cdcOptions(p.settings.Backends.CDC),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build snapshot backend registry: %w", err)
	}
	return registry, nil
}

// cdcOptions translates the backends.cdc section into the backend's own options.
func cdcOptions(cfg CDCBackend) cdc.Options {
	return cdc.Options{
		Image:       cfg.Image,
		Incremental: cfg.RestoreMode == RestoreModeIncremental,
		Overwrite:   cfg.Overwrite,
	}
}
