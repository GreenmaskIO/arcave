// Package config defines GM's per-project configuration value type and the pure
// helpers that seed and resolve its defaults. Persistence lives in
// internal/repository/config; reading is handled by internal/appconfig (viper).
package config

import (
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/utils"
)

const (
	gmDir    = ".gm"
	fileName = "config.yaml"
)

// Config is the content persisted to .gm/config.yaml by init. The yaml tags
// drive Save; the mapstructure tags drive viper.Unmarshal in appconfig.Resolve.
type Config struct {
	Name         string   `yaml:"name" mapstructure:"name"`
	ComposeFiles []string `yaml:"compose_files" mapstructure:"compose_files"`
	SavepointDir string   `yaml:"savepoint_dir" mapstructure:"savepoint_dir"`
	// Log holds the project's default logging settings, honoured by every
	// command unless overridden by the --log-level/--log-format global flags.
	Log Log `yaml:"log,omitempty" mapstructure:"log"`
}

// Log is the project's default logging configuration, persisted under the "log"
// key in .gm/config.yaml.
type Log struct {
	Level  string `yaml:"level,omitempty" mapstructure:"level"`
	Format string `yaml:"format,omitempty" mapstructure:"format"`
}

// NewConfig returns a Config seeded with the static defaults. The computed
// defaults that depend on the resolved project name (Name, SavepointDir) are
// filled later by ApplyDefaults, once file/env/flags have been merged in.
func NewConfig() Config {
	return Config{
		Log: Log{
			Level:  utils.DefaultLogLevel,
			Format: utils.DefaultLogFormat,
		},
	}
}

// ApplyDefaults fills the computed defaults that depend on the resolved project
// name. It mutates the receiver, so callers pass the address of a local Config;
// no pointer escapes. It is the single source of these defaults, shared by
// Resolve (read path) and init (write path).
func (c *Config) ApplyDefaults(projectRoot string) {
	if c.Name == "" {
		c.Name = filepath.Base(projectRoot)
	}
	if c.SavepointDir == "" {
		c.SavepointDir = filepath.Join(gmDir, "savepoints", c.Name)
	}
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, gmDir, fileName)
}
