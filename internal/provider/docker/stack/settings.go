package stack

import (
	"fmt"
	"net"
	"time"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// storageTypeDirectory names the local-filesystem arm of the storage union, the
// only arm implemented.
const storageTypeDirectory = "directory"

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
	// RestoreModeWipe clears the mount and restores it whole.
	RestoreModeWipe = "wipe"
	// RestoreModeIncremental restores over the live data, writing only what
	// differs and deleting what the snapshot does not carry. The default.
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

// Where cdc's restic server lives: the two ends of one loopback hop.
const (
	// DefaultCDCServerAddress binds the loopback on a port the kernel picks, so
	// two control planes cannot collide.
	DefaultCDCServerAddress = "127.0.0.1:0"
	// DefaultCDCContainerHost is how a container on Docker Desktop names the host
	// loopback. Native Linux does not resolve it.
	DefaultCDCContainerHost = "host.docker.internal"
	// DefaultCDCServerScheme is the scheme the container fetches over; arcave's
	// own server speaks plain HTTP.
	DefaultCDCServerScheme = SchemeHTTP
)

// The schemes backends.cdc.server_scheme accepts.
const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// The bounds a provider gives compose when it stops and starts the stack.
const (
	// DefaultWaitTimeout bounds the health-check wait after a start; without it a
	// crash-looping container would block a savepoint forever.
	DefaultWaitTimeout = 120 * time.Second
	// DefaultStopTimeout is the grace period a container gets before SIGKILL. A
	// database killed mid-shutdown is captured mid-shutdown.
	DefaultStopTimeout = 10 * time.Second
)

// Which channel compose's own progress goes out on — where the events go, not
// how they are rendered, which is log.format's business.
const (
	// ProgressLog turns each compose event into an arcave log record. The default.
	ProgressLog = "log"
	// ProgressTTY hands the terminal to compose's own renderer instead.
	ProgressTTY = "tty"
	// ProgressQuiet shows nothing, leaving only arcave's own log lines.
	ProgressQuiet = "quiet"
)

// Settings is everything a docker-compose stack provider owns — the whole
// `settings` section of its registry entry, which reaches it as an opaque blob.
type Settings struct {
	// Connection is how this provider reaches its daemon.
	Connection Docker `mapstructure:"connection"`
	// Compose is how patient it is with the stack once it has it.
	Compose Compose `mapstructure:"compose"`
	// Snapshot selects the backend a new savepoint is captured with.
	Snapshot Snapshot `mapstructure:"snapshot"`
	// Storage is where the captures and their records live.
	Storage Storage `mapstructure:"storage"`
	// Backends holds one section per snapshot backend.
	Backends Backends `mapstructure:"backends"`
}

// ApplyDefaults fills the values the provider acts on where the entry names none.
func (s *Settings) ApplyDefaults() {
	if s.Compose.WaitTimeout == 0 {
		s.Compose.WaitTimeout = DefaultWaitTimeout
	}
	if s.Compose.StopTimeout == 0 {
		s.Compose.StopTimeout = DefaultStopTimeout
	}
	if s.Compose.Progress == "" {
		s.Compose.Progress = ProgressLog
	}
	if s.Storage.Type == "" {
		s.Storage.Type = storageTypeDirectory
	}
	if s.Snapshot.Backend == "" {
		s.Snapshot.Backend = SnapshotBackendFullCopy
	}
	s.Backends.CDC.ApplyDefaults()
}

// Validate checks the settings the provider must act on.
func (s Settings) Validate() error {
	if err := s.Compose.Validate(); err != nil {
		return err
	}
	if err := s.Storage.Validate(); err != nil {
		return err
	}
	if err := s.Snapshot.Validate(); err != nil {
		return err
	}
	return s.Backends.Validate()
}

// Docker is how a provider reaches its daemon, honoured by both halves of the
// docker client. An empty Host means "read the environment".
type Docker struct {
	Host       string `mapstructure:"host"`
	APIVersion string `mapstructure:"api_version"`
	Context    string `mapstructure:"context"`
	// CertPath is a directory holding ca.pem/cert.pem/key.pem; the three fields
	// below override an individual file that does not follow those names.
	CertPath  string `mapstructure:"cert_path"`
	CAFile    string `mapstructure:"ca_file"`
	CertFile  string `mapstructure:"cert_file"`
	KeyFile   string `mapstructure:"key_file"`
	TLSVerify bool   `mapstructure:"tls_verify"`
}

// Compose is how patient a provider is with the stack: the two bounds it gives
// `docker compose stop`/`up`, and how loudly compose reports its own progress.
type Compose struct {
	// WaitTimeout bounds the health-check wait after a start. A request naming
	// its own bound overrides it.
	WaitTimeout time.Duration `mapstructure:"wait_timeout"`
	// StopTimeout is the grace period before SIGKILL. Zero means "unset", so an
	// immediate kill is spelled 1s.
	StopTimeout time.Duration `mapstructure:"stop_timeout"`
	// Progress selects the channel compose's own progress goes out on, not its
	// format — a log record is rendered by log.format like every other.
	Progress string `mapstructure:"progress"`
}

// Validate rejects bounds a provider cannot act on and a progress mode compose
// does not know. "Wait forever" is not on offer.
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
// present. The arms are pointers so an unset one stays nil.
type Storage struct {
	Type      string            `mapstructure:"type"`
	Directory *DirectoryStorage `mapstructure:"directory"`
}

// DirectoryStorage is the local-filesystem arm. Path is rooted under the static
// arcave namespace by the runtime's internal/store.
type DirectoryStorage struct {
	Path string `mapstructure:"path"`
}

// Validate rejects a union whose tag names an absent or unimplemented arm.
func (s Storage) Validate() error {
	switch s.Type {
	case storageTypeDirectory:
		if s.Directory == nil {
			return fmt.Errorf("storage.type is %q but no storage.directory section is present", s.Type)
		}
		if s.Directory.Path == "" {
			return fmt.Errorf("storage.directory.path is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported storage.type %q (supported: %s)", s.Type, storageTypeDirectory)
	}
}

// Snapshot selects the backend a *new* savepoint uses; a rollback reads the
// backend out of the record it restores.
type Snapshot struct {
	Backend string `mapstructure:"backend"`
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
// the one place the two vocabularies meet.
func (s Snapshot) BackendKind() dockermodel.SnapshotBackendKind {
	switch s.Backend {
	case SnapshotBackendFullCopy:
		return dockermodel.SnapshotBackendCopy
	case SnapshotBackendCDC:
		return dockermodel.SnapshotBackendCDC
	default:
		return dockermodel.SnapshotBackendUnknown
	}
}

// Backends is one section per snapshot backend.
type Backends struct {
	CDC CDCBackend `mapstructure:"cdc"`
}

// Validate checks every backend's section, present in the file or defaulted.
func (b Backends) Validate() error {
	return b.CDC.Validate()
}

// CDCBackend configures the content-defined-chunking backend. It is resolved
// whatever snapshot.backend says: a rollback obeys the record, not the config.
type CDCBackend struct {
	// RestoreMode is wipe (clear the mount, then restore it whole) or incremental
	// (restore over the live data, letting restic write only the difference).
	RestoreMode string `mapstructure:"restore_mode"`
	// Overwrite governs an incremental restore only.
	Overwrite string `mapstructure:"overwrite"`
	// Image is the restic image every cdc command runs in.
	Image string `mapstructure:"image"`
	// ServerAddress is what the provider's restic server binds. Port 0 lets the
	// kernel pick.
	ServerAddress string `mapstructure:"server_address"`
	// ServerScheme is the scheme of the repository URL: http, or https for a TLS
	// proxy fronting the server.
	ServerScheme string `mapstructure:"server_scheme"`
	// ContainerHost is that server's host name as the container sees it, never
	// the address arcave binds: a container cannot reach 127.0.0.1.
	ContainerHost string `mapstructure:"container_host"`
}

// ApplyDefaults fills the section whether or not the file carries one.
func (c *CDCBackend) ApplyDefaults() {
	if c.RestoreMode == "" {
		c.RestoreMode = RestoreModeIncremental
	}
	if c.Overwrite == "" {
		c.Overwrite = OverwriteAlways
	}
	if c.Image == "" {
		c.Image = DefaultResticImage
	}
	if c.ServerAddress == "" {
		c.ServerAddress = DefaultCDCServerAddress
	}
	if c.ServerScheme == "" {
		c.ServerScheme = DefaultCDCServerScheme
	}
	if c.ContainerHost == "" {
		c.ContainerHost = DefaultCDCContainerHost
	}
}

// Validate rejects a mode or an overwrite policy the backend cannot act on, and
// a server address it cannot bind.
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
	if c.ServerAddress == "" {
		return fmt.Errorf("backends.cdc.server_address is required")
	}
	// Split here rather than at listen time, halfway through wiring a provider.
	if _, _, err := net.SplitHostPort(c.ServerAddress); err != nil {
		return fmt.Errorf("backends.cdc.server_address %q is not a host:port address: %w", c.ServerAddress, err)
	}
	switch c.ServerScheme {
	case SchemeHTTP, SchemeHTTPS:
	default:
		return fmt.Errorf("unsupported backends.cdc.server_scheme %q (supported: %s, %s)",
			c.ServerScheme, SchemeHTTP, SchemeHTTPS)
	}
	if c.ContainerHost == "" {
		return fmt.Errorf("backends.cdc.container_host is required")
	}
	return nil
}
