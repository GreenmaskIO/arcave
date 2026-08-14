package main

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	cpconfig "github.com/greenmaskio/arcave/internal/config/controlplane"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	dockerstack "github.com/greenmaskio/arcave/internal/provider/docker/stack"
)

// stackFactories is the one place a provider type token becomes a constructor —
// the composition root's job, and the only file naming a provider package.
var stackFactories = map[string]provider.Factory{
	string(model.ProviderCompose): func(ctx context.Context, settings model.RawSettings) (provider.StackProvider, error) {
		return dockerstack.New(ctx, settings)
	},
}

// registry holds the registered stack providers by name.
type registry map[string]provider.StackProvider

// newRegistry builds one provider per providers.stacks entry. A provider that
// cannot be built fails the whole registry, releasing whatever was already open.
func newRegistry(ctx context.Context, cfg cpconfig.Providers) (registry, error) {
	r := make(registry, len(cfg.Stacks))

	for _, sp := range cfg.Stacks {
		newProvider, ok := stackFactories[sp.Type]
		if !ok {
			r.Close(ctx)
			return nil, fmt.Errorf("build provider %q: unsupported type %q", sp.Name, sp.Type)
		}
		p, err := newProvider(ctx, sp.Settings)
		if err != nil {
			r.Close(ctx)
			return nil, fmt.Errorf("build provider %q: %w", sp.Name, err)
		}
		r[sp.Name] = p
	}
	return r, nil
}

// Close releases every provider, logging rather than stopping at the first
// failure. The order is the map's: providers share nothing to unwind.
func (r registry) Close(ctx context.Context) {
	for name, p := range r {
		if err := p.Close(ctx); err != nil {
			log.Ctx(ctx).Error().Err(err).Str("provider", name).Msg("close provider")
		}
		delete(r, name)
	}
}
