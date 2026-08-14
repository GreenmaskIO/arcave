// Package config is arcave's per-project configuration and Unmarshal, the single
// home of the config policy. `project` is sent; `control_plane` and `log` are local.
package config

import (
	"fmt"
	"path/filepath"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

const fileName = "config.yaml"

// DefaultControlPlaneAddress is where the control plane listens locally.
const DefaultControlPlaneAddress = "http://127.0.0.1:7654"

// Config is the content persisted to .arcave/config.yaml by init. The yaml tags
// drive Save; the mapstructure tags drive Unmarshal.
type Config struct {
	Project Project `yaml:"project" mapstructure:"project"`
	// ControlPlane is how this CLI reaches the control plane. It is local: the
	// section never travels to the service.
	ControlPlane ControlPlane `yaml:"control_plane" mapstructure:"control_plane"`
	// Log holds the project's default logging settings, honoured by every
	// command unless overridden by the --log-level/--log-format global flags.
	Log Log `yaml:"log,omitempty" mapstructure:"log"`
}

// Project is the declaration `arcave project apply` sends: everything under it
// is applied, and nothing under it is interpreted by the client.
type Project struct {
	Name      string    `yaml:"name" mapstructure:"name"`
	Providers Providers `yaml:"providers" mapstructure:"providers"`
}

// Providers names the registered providers the project may use, by id and never
// by type. Whether an id is real, and allowed here, is the control plane's.
type Providers struct {
	// Sandbox is the provider the project's sandbox runs on, if it has one.
	Sandbox string `yaml:"sandbox,omitempty" mapstructure:"sandbox"`
	// Stacks are the providers the project's stacks may live on, and the bound:
	// the control plane serves this project nothing not named here.
	Stacks []string `yaml:"stacks,omitempty" mapstructure:"stacks"`
}

// Validate gates the name, which addresses the project in every request: empty
// means the directory normalized to nothing. The provider ids are not judged.
func (p Project) Validate() error {
	if err := model.ProjectName(p.Name).Validate(); err != nil {
		return fmt.Errorf("project.name: %w (set project.name explicitly)", err)
	}
	return nil
}

// ControlPlane is the control plane endpoint and the credential for it.
type ControlPlane struct {
	Address string `yaml:"address" mapstructure:"address"`
	Token   string `yaml:"token,omitempty" mapstructure:"token"`
}

// Validate rejects an address no command could reach. ApplyDefaults fills it
// unconditionally, so only a Config assembled in code reaches this empty.
func (c ControlPlane) Validate() error {
	if c.Address == "" {
		return fmt.Errorf("control_plane.address is required")
	}
	return nil
}

// Log is the project's default logging configuration, persisted under the "log"
// key in .arcave/config.yaml.
type Log struct {
	Level  string `yaml:"level,omitempty" mapstructure:"level"`
	Format string `yaml:"format,omitempty" mapstructure:"format"`
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

// ApplyDefaults fills the defaults that depend on the resolved project name,
// plus the local endpoint. Shared by the read path and by init.
func (c *Config) ApplyDefaults(projectRoot string) {
	if c.Project.Name == "" {
		// Normalized rather than verbatim: MyApp yields myapp. A basename that
		// normalizes to nothing stays empty, and Validate reports it.
		c.Project.Name = model.NormalizeProjectName(filepath.Base(projectRoot)).String()
	}
	if c.ControlPlane.Address == "" {
		c.ControlPlane.Address = DefaultControlPlaneAddress
	}
}

// Validate checks the sections whose values arcave must be able to act on. It runs
// after ApplyDefaults, so an empty section is a default rather than an error.
func (c *Config) Validate() error {
	if err := c.Project.Validate(); err != nil {
		return err
	}
	return c.ControlPlane.Validate()
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, model.StateDir, fileName)
}
