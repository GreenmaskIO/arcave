// Package config defines GM's per-project configuration value type, the pure
// helpers that seed and validate it, and Unmarshal — the single home of the
// config policy (version gate, unknown-key rejection, defaults, validation).
// Persistence lives in internal/repository/config; the multi-source merge
// (flag > env > file) is cmd/gm's, which hands the loaded viper here.
//
// The config *is* its viper decode — the mapstructure tags below are the schema
// viper fills — so Unmarshal takes a *viper.Viper rather than re-implementing
// that decode over a settings map. The pluggable sections still declare plain
// local union arms rather than embedding directory.Config or a future s3.Config:
// embedding would drag a cloud SDK into the import graph of repository/config,
// and therefore of `gm init`. cmd/gm translates each section into its package's
// own options struct.
package config

import (
	"fmt"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/utils"
)

const (
	gmDir    = ".gm"
	fileName = "config.yaml"
)

// Version is the schema version of .gm/config.yaml, and the only one GM speaks.
// A file naming any other is refused — see ErrUnsupportedConfigVersion.
const Version = 1

// StorageTypeDirectory names the local-filesystem arm of the storage union. It
// is the only arm implemented today; the union exists so adding "s3" is a new
// arm rather than a new set of top-level keys.
const StorageTypeDirectory = "directory"

// SnapshotBackendFullCopy names the MVP snapshot backend, which stores each
// mount's tar stream verbatim.
const SnapshotBackendFullCopy = "fullcopy"

// Config is the content persisted to .gm/config.yaml by init. The yaml tags
// drive Save; the mapstructure tags drive Unmarshal.
// One section per subsystem: a new subsystem adds a section rather than another
// top-level key.
type Config struct {
	Version  int      `yaml:"version" mapstructure:"version"`
	Project  Project  `yaml:"project" mapstructure:"project"`
	Docker   Docker   `yaml:"docker,omitempty" mapstructure:"docker"`
	Storage  Storage  `yaml:"storage" mapstructure:"storage"`
	Snapshot Snapshot `yaml:"snapshot" mapstructure:"snapshot"`
	// Log holds the project's default logging settings, honoured by every
	// command unless overridden by the --log-level/--log-format global flags.
	Log Log `yaml:"log,omitempty" mapstructure:"log"`
}

// Project identifies the compose project GM manages.
type Project struct {
	Name         string   `yaml:"name" mapstructure:"name"`
	ComposeFiles []string `yaml:"compose_files" mapstructure:"compose_files"`
}

// Docker is how GM reaches the daemon. It is honoured by both halves of the
// docker client — the SDK (Engine) and the CLI (ComposeRunner) — so the two can
// never silently target different daemons. An empty Host means "read the
// environment", the historical behaviour.
type Docker struct {
	Host       string `yaml:"host,omitempty" mapstructure:"host"`
	APIVersion string `yaml:"api_version,omitempty" mapstructure:"api_version"`
	Context    string `yaml:"context,omitempty" mapstructure:"context"`
	CertPath   string `yaml:"cert_path,omitempty" mapstructure:"cert_path"`
	TLSVerify  bool   `yaml:"tls_verify,omitempty" mapstructure:"tls_verify"`
}

// Storage is a tagged union: Type names the arm, and exactly that arm must be
// present. The arms are pointers so an unset one is omitted on marshal rather
// than written out as a block of zero values.
type Storage struct {
	Type      string            `yaml:"type" mapstructure:"type"`
	Directory *DirectoryStorage `yaml:"directory,omitempty" mapstructure:"directory"`
}

// DirectoryStorage is the local-filesystem arm. Path is resolved against the
// project root when relative, and validated, by internal/storages.New — which is
// the only place that turns it into a Storager.
type DirectoryStorage struct {
	Path string `yaml:"path" mapstructure:"path"`
}

// Validate rejects a union whose tag names an arm that is not present, or an arm
// GM does not implement.
func (s Storage) Validate() error {
	switch s.Type {
	case StorageTypeDirectory:
		if s.Directory == nil {
			return fmt.Errorf("storage.type is %q but no storage.directory section is present", s.Type)
		}
		if s.Directory.Path == "" {
			return fmt.Errorf("storage.directory.path is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported storage.type %q (supported: %s)", s.Type, StorageTypeDirectory)
	}
}

// DirectoryPath returns the directory arm's path, or "" when the union does not
// name that arm. A caller that only wants to *read* the savepoint directory (a
// status report; a test asserting on a resolved config) uses this rather than
// dereferencing an arm a hand-written config may not carry. Nothing turns the
// path into a store this way — that is storages.New's alone.
func (s Storage) DirectoryPath() string {
	if s.Directory == nil {
		return ""
	}
	return s.Directory.Path
}

// Snapshot selects the snapshot backend that owns the read→write pipeline.
type Snapshot struct {
	Backend string `yaml:"backend" mapstructure:"backend"`
}

// Validate rejects a backend GM cannot construct.
func (s Snapshot) Validate() error {
	if s.Backend != SnapshotBackendFullCopy {
		return fmt.Errorf("unsupported snapshot.backend %q (supported: %s)", s.Backend, SnapshotBackendFullCopy)
	}
	return nil
}

// Log is the project's default logging configuration, persisted under the "log"
// key in .gm/config.yaml.
type Log struct {
	Level  string `yaml:"level,omitempty" mapstructure:"level"`
	Format string `yaml:"format,omitempty" mapstructure:"format"`
}

// NewConfig returns a Config seeded with the static defaults, Version included:
// a fresh project is already at the current schema. A file's own version never
// reaches this value — Unmarshal gates on it through a separate probe, before
// any decode into Config.
func NewConfig() Config {
	return Config{
		Version: 1,
		Log: Log{
			Level:  utils.DefaultLogLevel,
			Format: utils.DefaultLogFormat,
		},
	}
}

// ApplyDefaults fills the computed defaults that depend on the resolved project
// name, plus the tags of the pluggable sections. It mutates the receiver, so
// callers pass the address of a local Config; no pointer escapes. It is the
// single source of these defaults, shared by resolveConfig (read path) and init
// (write path).
func (c *Config) ApplyDefaults(projectRoot string) {
	if c.Version == 0 {
		c.Version = Version
	}
	if c.Project.Name == "" {
		c.Project.Name = filepath.Base(projectRoot)
	}
	if c.Storage.Type == "" {
		c.Storage.Type = StorageTypeDirectory
	}
	if c.Storage.Type == StorageTypeDirectory {
		// Fill a copy of the arm and re-point at it, never through the caller's
		// pointer: a Config is passed around by value (InitService.Init takes
		// one), and mutating the pointee would reach back into the caller's own
		// value across that copy.
		dir := DirectoryStorage{}
		if c.Storage.Directory != nil {
			dir = *c.Storage.Directory
		}
		if dir.Path == "" {
			dir.Path = filepath.Join(gmDir, "savepoints", c.Project.Name)
		}
		c.Storage.Directory = &dir
	}
	if c.Snapshot.Backend == "" {
		c.Snapshot.Backend = SnapshotBackendFullCopy
	}
}

// Validate checks the sections whose values GM must be able to act on. It runs
// after ApplyDefaults, so an empty section is a default rather than an error.
func (c *Config) Validate() error {
	if c.Version != Version {
		return model.ErrUnsupportedConfigVersion
	}
	if err := c.Storage.Validate(); err != nil {
		return err
	}
	return c.Snapshot.Validate()
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, gmDir, fileName)
}
