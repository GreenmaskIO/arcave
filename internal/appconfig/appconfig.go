// Package appconfig resolves GM's effective configuration for a command run.
// Following Greenmask v1, each command binds its own flags to the resolver where
// those flags are defined (Resolver.Bind), and Resolve then merges the config
// file, the GM_* environment, and the bound flags into a config.Config. The
// resolver owns a viper instance so bindings registered at command-construction
// time are read back after cobra parses the flags. It knows nothing of cobra, so
// the MCP adapter can reuse it too.
package appconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/greenmaskio/gm-tools/internal/config"
)

// Resolver merges the config file, environment, and bound command flags into a
// config.Config for the project rooted at projectRoot.
type Resolver struct {
	v           *viper.Viper
	projectRoot string
}

// NewResolver builds a resolver for the project rooted at projectRoot.
// Environment variables are read as GM_<KEY> with dots mapped to underscores, so
// GM_LOG_LEVEL feeds the "log.level" key.
func NewResolver(projectRoot string) *Resolver {
	v := viper.New()
	v.SetConfigFile(config.Path(projectRoot))
	v.SetConfigType("yaml")
	v.SetEnvPrefix("GM")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	return &Resolver{v: v, projectRoot: projectRoot}
}

// Bind binds the named flag to the given config key (e.g. "log-level" ->
// "log.level", "file" -> "compose_files"). Call it right where the flag is
// defined; Resolve then reads the bound flag with viper precedence — a changed
// flag > env > file > the flag's default. A missing flag is a wiring bug, so it
// panics rather than silently dropping the binding.
func (r *Resolver) Bind(flags *pflag.FlagSet, name, key string) {
	flag := flags.Lookup(name)
	if flag == nil {
		panic(fmt.Sprintf("appconfig: cannot bind config key %q: no flag %q", key, name))
	}
	if err := r.v.BindPFlag(key, flag); err != nil {
		panic(fmt.Sprintf("appconfig: bind %q -> %q: %v", name, key, err))
	}
}

// ConfigPath returns the config file path the resolver reads.
func (r *Resolver) ConfigPath() string {
	return config.Path(r.projectRoot)
}

// Resolve reads the config file (if present), overlays the environment and bound
// flags, and fills the name-dependent computed defaults. found is false when the
// config file is absent — a normal case for `gm init` (it proceeds on defaults
// plus flags) and an error the read commands report.
func (r *Resolver) Resolve() (cfg config.Config, found bool, err error) {
	found = true
	if err := r.v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, os.ErrNotExist) {
			found = false
		} else {
			return config.Config{}, false, fmt.Errorf("read config: %w", err)
		}
	}

	cfg = config.NewConfig()
	if err := r.v.Unmarshal(&cfg); err != nil {
		return config.Config{}, false, fmt.Errorf("parse config: %w", err)
	}
	cfg.ApplyDefaults(r.projectRoot) // mutates the local cfg through its address
	return cfg, found, nil
}
