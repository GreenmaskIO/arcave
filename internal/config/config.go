// Package config defines arcave's per-project configuration value type, the pure
// helpers that seed and validate it, and Unmarshal — the single home of the
// config policy (version gate, unknown-key rejection, defaults, validation).
// Persistence lives in internal/repository/config; the multi-source merge
// (flag > env > file) is cmd/arcave's, which hands the loaded viper here.
//
// The config *is* its viper decode — the mapstructure tags below are the schema
// viper fills — so Unmarshal takes a *viper.Viper rather than re-implementing
// that decode over a settings map. The pluggable sections still declare plain
// local union arms rather than embedding directory.Config or a future s3.Config:
// embedding would drag a cloud SDK into the import graph of repository/config,
// and therefore of `arcave init`. cmd/arcave translates each section into its package's
// own options struct.
package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

const fileName = "config.yaml"

// Version is the schema version of .arcave/config.yaml, and the only one arcave speaks.
// A file naming any other is refused — see ErrUnsupportedConfigVersion.
const Version = 1

// StorageTypeDirectory names the local-filesystem arm of the storage union. It
// is the only arm implemented today; the union exists so adding "s3" is a new
// arm rather than a new set of top-level keys.
const StorageTypeDirectory = "directory"

// SnapshotBackendFullCopy names the MVP snapshot backend, which stores each
// mount's tar stream verbatim.
const SnapshotBackendFullCopy = "fullcopy"

// The bounds arcave gives compose when it stops and starts the stack. They live
// here rather than in the compose client because they are project config now:
// the client applies them, but the user names them, and cmd/arcave advertises
// them as the --wait-timeout/--stop-timeout defaults.
const (
	// DefaultWaitTimeout bounds the health-check wait after a start. Without a
	// bound a service that never becomes healthy (a crash-looping container with
	// restart: always) would block a savepoint forever.
	DefaultWaitTimeout = 120 * time.Second
	// DefaultStopTimeout is the grace period a container gets to exit before
	// compose sends SIGKILL. It matters more here than it does to `docker compose
	// stop`: arcave snapshots the volumes the moment the containers are down, so a
	// database killed mid-shutdown is captured mid-shutdown.
	DefaultStopTimeout = 10 * time.Second
)

// Which channel compose's own progress goes out on. These are arcave's values,
// not compose's `--progress` ones: the choice here is *where* the events go, and
// how they are rendered once they get there is log.format's business.
const (
	// ProgressLog turns each compose event into an arcave log record, so the run
	// has one output channel in one format. The default.
	ProgressLog = "log"
	// ProgressTTY hands the terminal to compose's own renderer instead — the
	// familiar redrawing progress display, at the cost of a second format on
	// stderr.
	ProgressTTY = "tty"
	// ProgressQuiet shows nothing, leaving only arcave's own log lines.
	ProgressQuiet = "quiet"
)

// Config is the content persisted to .arcave/config.yaml by init. The yaml tags
// drive Save; the mapstructure tags drive Unmarshal.
// One section per subsystem: a new subsystem adds a section rather than another
// top-level key.
type Config struct {
	Version  int      `yaml:"version" mapstructure:"version"`
	Project  Project  `yaml:"project" mapstructure:"project"`
	Docker   Docker   `yaml:"docker,omitempty" mapstructure:"docker"`
	Compose  Compose  `yaml:"compose" mapstructure:"compose"`
	Storage  Storage  `yaml:"storage" mapstructure:"storage"`
	Snapshot Snapshot `yaml:"snapshot" mapstructure:"snapshot"`
	// Log holds the project's default logging settings, honoured by every
	// command unless overridden by the --log-level/--log-format global flags.
	Log Log `yaml:"log,omitempty" mapstructure:"log"`
}

// Project identifies the compose project arcave manages.
type Project struct {
	Name         string   `yaml:"name" mapstructure:"name"`
	ComposeFiles []string `yaml:"compose_files" mapstructure:"compose_files"`
}

// Docker is how arcave reaches the daemon. It is honoured by both halves of the
// docker client — the SDK (Engine) and the CLI (ComposeRunner) — so the two can
// never silently target different daemons. An empty Host means "read the
// environment", the historical behaviour.
type Docker struct {
	Host       string `yaml:"host,omitempty" mapstructure:"host"`
	APIVersion string `yaml:"api_version,omitempty" mapstructure:"api_version"`
	Context    string `yaml:"context,omitempty" mapstructure:"context"`
	// CertPath is a directory holding ca.pem/cert.pem/key.pem, the
	// DOCKER_CERT_PATH convention. The three CertFile/CAFile/KeyFile fields below
	// override an individual file when a certificate does not follow those names.
	CertPath  string `yaml:"cert_path,omitempty" mapstructure:"cert_path"`
	CAFile    string `yaml:"ca_file,omitempty" mapstructure:"ca_file"`
	CertFile  string `yaml:"cert_file,omitempty" mapstructure:"cert_file"`
	KeyFile   string `yaml:"key_file,omitempty" mapstructure:"key_file"`
	TLSVerify bool   `yaml:"tls_verify,omitempty" mapstructure:"tls_verify"`
}

// Compose is how arcave drives the project's lifecycle — the two bounds it gives
// `docker compose stop`/`up`, and how loudly compose reports its own progress.
// Unlike Docker, which says *which* daemon to reach, this section says how
// patient arcave is with the stack once it has it.
//
// It is a section of its own rather than fields on Project because Project
// identifies the stack (its name, its files) while these are the knobs a user
// turns when a service is slow: a savepoint that reports nothing for two minutes
// and a database killed mid-shutdown are both fixed from here.
type Compose struct {
	// WaitTimeout bounds the health-check wait after a start. Every command that
	// restarts the stack advertises it as --wait-timeout; a request naming its own
	// bound (an MCP tool call) overrides it.
	WaitTimeout time.Duration `yaml:"wait_timeout" mapstructure:"wait_timeout"`
	// StopTimeout is how long a container gets to exit on its own before compose
	// sends SIGKILL. Zero means "unset": DefaultStopTimeout applies, so an
	// immediate kill is spelled 1s, not 0 — the value that reads as "no grace at
	// all" is the one a user is most likely to leave unset by accident, and a
	// SIGKILLed database is exactly what this bound exists to prevent.
	StopTimeout time.Duration `yaml:"stop_timeout" mapstructure:"stop_timeout"`
	// Progress selects the channel compose's own progress goes out on: folded
	// into arcave's log (the default), rendered by compose itself, or dropped.
	// It is not a choice of *format* — a log record is rendered by log.format
	// like every other. Either way it stays off stdout, which carries nothing
	// but arcave's JSON result.
	Progress string `yaml:"progress" mapstructure:"progress"`
}

// Validate rejects bounds arcave cannot act on and a progress mode compose does
// not know. A wait timeout of zero never reaches here — ApplyDefaults fills it —
// so a non-positive value is one the user wrote deliberately, and "wait forever"
// is not on offer: it is the failure mode the bound exists to turn into an error.
func (c Compose) Validate() error {
	if c.WaitTimeout <= 0 {
		return fmt.Errorf("compose.wait_timeout must be a positive duration, got %s", c.WaitTimeout)
	}
	if c.StopTimeout < 0 {
		return fmt.Errorf("compose.stop_timeout must not be negative, got %s", c.StopTimeout)
	}
	switch c.Progress {
	case ProgressLog, ProgressTTY, ProgressQuiet:
		return nil
	default:
		return fmt.Errorf("unsupported compose.progress %q (supported: %s, %s, %s)",
			c.Progress, ProgressLog, ProgressTTY, ProgressQuiet)
	}
}

// Storage is a tagged union: Type names the arm, and exactly that arm must be
// present. The arms are pointers so an unset one is omitted on marshal rather
// than written out as a block of zero values.
type Storage struct {
	Type      string            `yaml:"type" mapstructure:"type"`
	Directory *DirectoryStorage `yaml:"directory,omitempty" mapstructure:"directory"`
}

// DirectoryStorage is the local-filesystem arm. Path is resolved against the
// project root when relative, and rooted under the static arcave namespace, by
// internal/storefactory.New — which is the only place that turns it into a
// Storager.
type DirectoryStorage struct {
	Path string `yaml:"path" mapstructure:"path"`
}

// Validate rejects a union whose tag names an arm that is not present, or an arm
// arcave does not implement.
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
// path into a store this way — that is storefactory.New's alone.
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

// Validate rejects a backend arcave cannot construct.
func (s Snapshot) Validate() error {
	if s.Backend != SnapshotBackendFullCopy {
		return fmt.Errorf("unsupported snapshot.backend %q (supported: %s)", s.Backend, SnapshotBackendFullCopy)
	}
	return nil
}

// Log is the project's default logging configuration, persisted under the "log"
// key in .arcave/config.yaml.
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
	if c.Compose.WaitTimeout == 0 {
		c.Compose.WaitTimeout = DefaultWaitTimeout
	}
	if c.Compose.StopTimeout == 0 {
		c.Compose.StopTimeout = DefaultStopTimeout
	}
	if c.Compose.Progress == "" {
		c.Compose.Progress = ProgressLog
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
			dir.Path = filepath.Join(model.StateDir, "storage", c.Project.Name)
		}
		c.Storage.Directory = &dir
	}
	if c.Snapshot.Backend == "" {
		c.Snapshot.Backend = SnapshotBackendFullCopy
	}
}

// Validate checks the sections whose values arcave must be able to act on. It runs
// after ApplyDefaults, so an empty section is a default rather than an error.
func (c *Config) Validate() error {
	if c.Version != Version {
		return model.ErrUnsupportedConfigVersion
	}
	if err := c.Compose.Validate(); err != nil {
		return err
	}
	if err := c.Storage.Validate(); err != nil {
		return err
	}
	return c.Snapshot.Validate()
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, model.StateDir, fileName)
}
