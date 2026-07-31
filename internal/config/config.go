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
	"net"
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

// The tokens snapshot.backend accepts.
const (
	// SnapshotBackendFullCopy stores each mount's tar stream verbatim.
	SnapshotBackendFullCopy = "fullcopy"
	// SnapshotBackendCDC captures into a restic repository that outlives any one
	// savepoint, writing only the chunks that changed.
	SnapshotBackendCDC = "cdc"
)

// The cdc backend's restore strategies, selected by backends.cdc.restore_mode.
const (
	// RestoreModeWipe clears the mount and restores it whole. The default.
	RestoreModeWipe = "wipe"
	// RestoreModeIncremental restores over the live data, writing only what
	// differs and deleting what the snapshot does not carry.
	RestoreModeIncremental = "incremental"
)

// What an incremental restore does with a file that is already there. Ignored by
// RestoreModeWipe.
const (
	// OverwriteIfChanged compares content and rewrites only what differs.
	OverwriteIfChanged = "if-changed"
	// OverwriteAlways rewrites every file the snapshot carries.
	OverwriteAlways = "always"
)

// DefaultResticImage is the stock image every cdc command runs in.
// --delete/--overwrite need restic ≥0.17.
const DefaultResticImage = "restic/restic:0.19.1"

// The sockets backends.cdc.data_plane.transport accepts.
const (
	// DataPlaneTransportTCP serves the repositories on a host port the container
	// reaches by name. The default, and the only implemented arm.
	DataPlaneTransportTCP = "tcp"
	// DataPlaneTransportUnix serves them on a unix socket bound into the
	// container. Not implemented: a project naming it fails at wiring time.
	DataPlaneTransportUnix = "unix"
)

// How the data plane authenticates a container. The vocabulary is restic's: it
// presents credentials through the URL's userinfo and can send no header of its
// own, so a bearer arm would be a key nobody could use.
const (
	// DataPlaneAuthNone serves whoever reaches the socket. The default, and the
	// only implemented arm.
	DataPlaneAuthNone = "none"
	// DataPlaneAuthBasic requires the username and password the repository URL
	// carries. Not implemented: a project naming it fails at wiring time.
	DataPlaneAuthBasic = "basic"
)

// The data plane's defaults, filled per transport by ApplyDefaults.
const (
	// DefaultDataPlaneAddress binds the loopback on a port the kernel picks. A
	// fixed port would collide between two arcave processes — a CLI run and an
	// `arcave mcp` server, or two projects — and give an attacker a predictable
	// target.
	DefaultDataPlaneAddress = "127.0.0.1:0"
	// DefaultDataPlaneContainerHost is how a container on Docker Desktop names
	// the host loopback. Native Linux does not resolve it, which is what makes
	// this key the one that gets cdc running there.
	DefaultDataPlaneContainerHost = "host.docker.internal"
	// DefaultDataPlaneContainerSocket is where the host's socket is bound inside
	// the restic container.
	DefaultDataPlaneContainerSocket = "/run/arcave.sock"
	// DefaultDataPlaneUsername is the user half of the generated credential.
	DefaultDataPlaneUsername = "arcave"
)

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
	// Backends holds the settings of the individual snapshot backends, one
	// section per backend, present only for the backends a project configures.
	Backends Backends `yaml:"backends,omitempty" mapstructure:"backends"`
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

// Snapshot selects the snapshot backend that owns the read→write pipeline. It
// says which backend a *new* savepoint uses; a rollback reads the backend out of
// the record it restores, so changing this never orphans an existing savepoint.
type Snapshot struct {
	Backend string `yaml:"backend" mapstructure:"backend"`
}

// Validate rejects a backend arcave cannot construct.
func (s Snapshot) Validate() error {
	switch s.Backend {
	case SnapshotBackendFullCopy, SnapshotBackendCDC:
		return nil
	default:
		return fmt.Errorf("unsupported snapshot.backend %q (supported: %s, %s)",
			s.Backend, SnapshotBackendFullCopy, SnapshotBackendCDC)
	}
}

// BackendKind translates the configured token into the kind a volume carries —
// the one place the two vocabularies meet. An unknown token yields
// SnapshotBackendUnknown, which the registry refuses by name; Validate has
// rejected it long before.
func (s Snapshot) BackendKind() model.SnapshotBackendKind {
	switch s.Backend {
	case SnapshotBackendFullCopy:
		return model.SnapshotBackendCopy
	case SnapshotBackendCDC:
		return model.SnapshotBackendCDC
	default:
		return model.SnapshotBackendUnknown
	}
}

// Backends is one optional section per snapshot backend. The arms are pointers
// so a project that never names a backend carries no section for it.
type Backends struct {
	CDC *CDCBackend `yaml:"cdc,omitempty" mapstructure:"cdc"`
}

// CDCBackend configures the content-defined-chunking backend: which restic image
// its one-shot containers run, and how a restore reaches the live mount.
type CDCBackend struct {
	// RestoreMode is wipe (clear the mount, then restore it whole) or incremental
	// (restore over the live data, letting restic write only the difference).
	RestoreMode string `yaml:"restore_mode" mapstructure:"restore_mode"`
	// Overwrite governs an incremental restore only.
	Overwrite string `yaml:"overwrite" mapstructure:"overwrite"`
	// Image is the restic image every cdc command runs in.
	Image string `yaml:"image" mapstructure:"image"`
	// DataPlane is the socket the restic containers reach their repositories
	// through, and how they authenticate on it.
	DataPlane DataPlane `yaml:"data_plane" mapstructure:"data_plane"`
}

// DataPlane configures the HTTP endpoint cdc's containers reach their
// repositories through: which socket it listens on, how a container names it,
// and how it authenticates. It is a tagged union in all but the tag's name —
// Transport selects the arm, and a key of the other arm is an error rather than
// a value silently ignored.
type DataPlane struct {
	// Transport is tcp (a host port) or unix (a socket file).
	Transport string `yaml:"transport" mapstructure:"transport"`
	// Address is the tcp arm's bind address. Port 0 lets the kernel pick.
	Address string `yaml:"address,omitempty" mapstructure:"address"`
	// ContainerHost is the tcp arm's host name *as the container sees it*, which
	// is not the address arcave binds: a container cannot reach 127.0.0.1.
	ContainerHost string `yaml:"container_host,omitempty" mapstructure:"container_host"`
	// Socket is the unix arm's socket path on the host.
	Socket string `yaml:"socket,omitempty" mapstructure:"socket"`
	// ContainerSocket is where that socket is bound inside the container — the
	// unix arm's counterpart to ContainerHost, and required for the same reason:
	// the path a container sees is not the path on the host.
	ContainerSocket string `yaml:"container_socket,omitempty" mapstructure:"container_socket"`
	// Auth is basic or none, and defaults to none.
	Auth string `yaml:"auth" mapstructure:"auth"`
	// Username is the basic-auth user.
	Username string `yaml:"username,omitempty" mapstructure:"username"`
	// Password is the basic-auth credential.
	Password string `yaml:"password,omitempty" mapstructure:"password"`
}

// Validate checks the arms that are present. An absent arm is not an error: a
// project that does not use a backend does not have to configure it.
func (b Backends) Validate() error {
	if b.CDC == nil {
		return nil
	}
	return b.CDC.Validate()
}

// Validate rejects a mode or an overwrite policy the backend cannot act on.
func (c CDCBackend) Validate() error {
	switch c.RestoreMode {
	case RestoreModeWipe, RestoreModeIncremental:
	default:
		return fmt.Errorf("unsupported backends.cdc.restore_mode %q (supported: %s, %s)",
			c.RestoreMode, RestoreModeWipe, RestoreModeIncremental)
	}
	switch c.Overwrite {
	case OverwriteIfChanged, OverwriteAlways:
	default:
		return fmt.Errorf("unsupported backends.cdc.overwrite %q (supported: %s, %s)",
			c.Overwrite, OverwriteIfChanged, OverwriteAlways)
	}
	if c.Image == "" {
		return fmt.Errorf("backends.cdc.image is required")
	}
	return c.DataPlane.Validate()
}

// Validate rejects a transport or an auth mode arcave cannot serve, and a key
// that belongs to the arm the transport does not name. An ignored key is not on
// offer here: a user who sets backends.cdc.data_plane.socket and gets a tcp
// endpoint anyway has been told nothing.
func (d DataPlane) Validate() error {
	switch d.Transport {
	case DataPlaneTransportTCP:
		if err := d.validateTCP(); err != nil {
			return err
		}
	case DataPlaneTransportUnix:
		if err := d.validateUnix(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported backends.cdc.data_plane.transport %q (supported: %s, %s)",
			d.Transport, DataPlaneTransportTCP, DataPlaneTransportUnix)
	}
	return d.validateAuth()
}

func (d DataPlane) validateTCP() error {
	if d.Socket != "" || d.ContainerSocket != "" {
		return fmt.Errorf("backends.cdc.data_plane.socket and container_socket belong to transport %s, but the transport is %s",
			DataPlaneTransportUnix, DataPlaneTransportTCP)
	}
	if d.Address == "" {
		return fmt.Errorf("backends.cdc.data_plane.address is required")
	}
	// The address is bound, so it must split into host and port here rather than
	// at listen time, halfway through wiring a command.
	if _, _, err := net.SplitHostPort(d.Address); err != nil {
		return fmt.Errorf("backends.cdc.data_plane.address %q is not a host:port address: %w", d.Address, err)
	}
	if d.ContainerHost == "" {
		return fmt.Errorf("backends.cdc.data_plane.container_host is required")
	}
	return nil
}

func (d DataPlane) validateUnix() error {
	if d.Address != "" || d.ContainerHost != "" {
		return fmt.Errorf("backends.cdc.data_plane.address and container_host belong to transport %s, but the transport is %s",
			DataPlaneTransportTCP, DataPlaneTransportUnix)
	}
	if d.ContainerSocket == "" {
		return fmt.Errorf("backends.cdc.data_plane.container_socket is required")
	}
	return nil
}

func (d DataPlane) validateAuth() error {
	switch d.Auth {
	case DataPlaneAuthNone:
		if d.Username != "" || d.Password != "" {
			return fmt.Errorf("backends.cdc.data_plane.username and password are set but auth is %s", DataPlaneAuthNone)
		}
		return nil
	case DataPlaneAuthBasic:
		if d.Username == "" {
			return fmt.Errorf("backends.cdc.data_plane.username is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported backends.cdc.data_plane.auth %q (supported: %s, %s)",
			d.Auth, DataPlaneAuthNone, DataPlaneAuthBasic)
	}
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
	c.applyCDCDefaults()
}

// applyCDCDefaults fills the cdc arm only for a project that has one or names cdc
// as its backend: a full-copy project should carry no block for a backend it
// never runs. Like the storage arm it fills a copy and re-points, since a Config
// travels by value and writing through the pointer would reach across the copy.
func (c *Config) applyCDCDefaults() {
	if c.Backends.CDC == nil && c.Snapshot.Backend != SnapshotBackendCDC {
		return
	}
	cdc := CDCBackend{}
	if c.Backends.CDC != nil {
		cdc = *c.Backends.CDC
	}
	if cdc.RestoreMode == "" {
		cdc.RestoreMode = RestoreModeWipe
	}
	if cdc.Overwrite == "" {
		cdc.Overwrite = OverwriteIfChanged
	}
	if cdc.Image == "" {
		cdc.Image = DefaultResticImage
	}
	cdc.DataPlane.applyDefaults()
	c.Backends.CDC = &cdc
}

// applyDefaults fills the data plane's keys for the transport it names, and only
// those: a key of the other arm is an error, so defaulting both arms would make
// every project fail its own validation. The receiver is already a copy of the
// arm applyCDCDefaults is filling, so mutating it in place reaches nothing.
//
// The default auth is none on both arms, since nothing implements another one.
func (d *DataPlane) applyDefaults() {
	if d.Transport == "" {
		d.Transport = DataPlaneTransportTCP
	}
	switch d.Transport {
	case DataPlaneTransportTCP:
		if d.Address == "" {
			d.Address = DefaultDataPlaneAddress
		}
		if d.ContainerHost == "" {
			d.ContainerHost = DefaultDataPlaneContainerHost
		}
	case DataPlaneTransportUnix:
		if d.ContainerSocket == "" {
			d.ContainerSocket = DefaultDataPlaneContainerSocket
		}
	}
	if d.Auth == "" {
		d.Auth = DataPlaneAuthNone
	}
	if d.Auth == DataPlaneAuthBasic && d.Username == "" {
		d.Username = DefaultDataPlaneUsername
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
	if err := c.Snapshot.Validate(); err != nil {
		return err
	}
	return c.Backends.Validate()
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, model.StateDir, fileName)
}
