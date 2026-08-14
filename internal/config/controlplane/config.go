// Package controlplane is the host-side config of the arcave control plane: the
// address it serves on and the provider registry. It names no provider's
// settings — a registry entry carries them as an opaque blob.
package controlplane

import (
	"fmt"
	"net"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// DefaultAPIAddress is the host half of the client's
// config.DefaultControlPlaneAddress.
const DefaultAPIAddress = "127.0.0.1:7654"

// Config is the content of control-plane.yaml. The mapstructure tags drive
// Unmarshal; nothing marshals this config, so it carries no yaml tags.
type Config struct {
	// API is the section describing the control plane itself.
	API API `mapstructure:"api"`
	// Providers is the registry: what the control plane can drive.
	Providers Providers `mapstructure:"providers"`
	// Log holds the defaults the --log-level/--log-format flags override.
	Log Log `mapstructure:"log"`
}

// API is where the control plane serves its own HTTP API.
type API struct {
	// Address is a host:port to bind — the server end of what a client knows as
	// control_plane.address.
	Address string `mapstructure:"address"`
}

// Validate rejects an address the server could not bind.
func (a API) Validate() error {
	if a.Address == "" {
		return fmt.Errorf("api.address is required")
	}
	// Split here rather than at listen time, halfway through starting a server.
	if _, _, err := net.SplitHostPort(a.Address); err != nil {
		return fmt.Errorf("api.address %q is not a host:port address: %w", a.Address, err)
	}
	return nil
}

// Providers is the registry, one list per provider family.
type Providers struct {
	Stacks []StackProvider `mapstructure:"stacks"`
}

// Validate gates a name per entry and refuses duplicates. Nothing below settings
// is judged here — that is the provider's.
func (p Providers) Validate() error {
	seen := make(map[string]struct{}, len(p.Stacks))
	for i, sp := range p.Stacks {
		if err := sp.Validate(); err != nil {
			return fmt.Errorf("providers.stacks[%d]: %w", i, err)
		}
		if _, dup := seen[sp.Name]; dup {
			return fmt.Errorf("providers.stacks[%d]: duplicate provider name %q", i, sp.Name)
		}
		seen[sp.Name] = struct{}{}
	}
	return nil
}

// StackProvider is one registered stack provider.
type StackProvider struct {
	// Name is how a request addresses this provider.
	Name string `mapstructure:"name"`
	// Type says which factory builds it. Which types exist is the registry's
	// knowledge, so an unknown one is refused there rather than here.
	Type string `mapstructure:"type"`
	// Settings is everything the provider owns, carried verbatim. The control
	// plane hands it to the factory and reads none of it — not its keys, not its
	// defaults, not its validity.
	Settings model.RawSettings `mapstructure:"settings"`
}

// Validate accepts an entry the control plane can hand to a factory.
func (s StackProvider) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if s.Type == "" {
		return fmt.Errorf("type is required")
	}
	return nil
}

// Log is the control plane's default logging configuration.
type Log struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// NewConfig returns a Config seeded with the static defaults.
func NewConfig() Config {
	return Config{
		Log: Log{
			Level:  utils.DefaultLogLevel,
			Format: utils.DefaultLogFormat,
		},
	}
}

// ApplyDefaults fills the endpoint. A provider's own defaults are the
// provider's, applied where its settings are decoded.
func (c *Config) ApplyDefaults() {
	if c.API.Address == "" {
		c.API.Address = DefaultAPIAddress
	}
}

// Validate checks the address the control plane binds and the shape of the
// registry. It stops at settings.
func (c *Config) Validate() error {
	if err := c.API.Validate(); err != nil {
		return err
	}
	return c.Providers.Validate()
}
