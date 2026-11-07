// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package config defines arcave's per-project configuration value type, the pure
// helpers that seed and validate it, and Unmarshal — the single home of the
// config policy (unknown-key rejection, defaults, validation).
// Persistence lives in internal/repository/config; the multi-source merge
// (flag > env > file) is cmd/arcave's, which hands the loaded viper here.
//
// The config *is* its viper decode — the mapstructure tags below are the schema
// viper fills — so Unmarshal takes a *viper.Viper rather than re-implementing
// that decode over a settings map. The pluggable sections still declare plain
// local union arms rather than embedding directory.Config or s3.Config:
// embedding would drag a cloud SDK into the import graph of repository/config,
// and therefore of `arcave init`. cmd/arcave translates each section into its package's
// own options struct.
package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

const fileName = "config.yaml"

// ErrInvalid reports a config value arcave cannot act on; every error Unmarshal
// returns wraps it.
var ErrInvalid = errors.New("invalid config")

// The tokens snapshot.backend accepts.
const (
	// SnapshotBackendFullCopy stores each mount's tar stream whole, in the format
	// backends.fullcopy.compression names.
	SnapshotBackendFullCopy = string(model.SnapshotBackendFullCopy)
	// SnapshotBackendCDC captures into a restic repository that outlives any one
	// savepoint, writing only the chunks that changed.
	SnapshotBackendCDC = string(model.SnapshotBackendCDC)
)

// The formats backends.fullcopy.compression accepts.
const (
	// CompressionZstd stores each archive under zstd at its default level. The default.
	CompressionZstd = "zstd"
	// CompressionNone stores each archive as the daemon hands it over.
	CompressionNone = "none"
)

// The cdc backend's restore strategies, selected by backends.cdc.restore_mode.
const (
	// RestoreModeWipe clears the mount and restores it whole.
	RestoreModeWipe = "wipe"
	// RestoreModeIncremental, the default, restores over the live data, writing only what
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

// The transports backends.cdc.transport accepts.
const (
	// CDCTransportStdio serves each repository over a docker exec stream into the
	// restic container: no listener, no network, any daemon arcave's API reaches.
	CDCTransportStdio = "stdio"
	// CDCTransportTCP serves the repositories on a loopback TCP server the container
	// dials, configured by server_address, server_scheme, container_host, network_mode.
	CDCTransportTCP = "tcp"
	// DefaultCDCTransport is the exec stream.
	DefaultCDCTransport = CDCTransportStdio
)

// Where cdc's restic server lives under the tcp transport: the two ends of one
// loopback hop.
const (
	// DefaultCDCServerAddress binds the loopback on a port the kernel picks. A
	// fixed port would collide between two arcave processes — a CLI run and an
	// `arcave mcp` server, or two projects — and give an attacker a predictable
	// target.
	DefaultCDCServerAddress = "127.0.0.1:0"
	// DefaultCDCContainerHost is how a container on Docker Desktop names the host
	// loopback. Only the bridge path uses it: a native Linux daemon, where the
	// name does not resolve, gets host networking instead (network_mode auto).
	DefaultCDCContainerHost = "host.docker.internal"
	// DefaultCDCServerScheme is the scheme the container fetches its repository
	// over. arcave's own server speaks plain HTTP; https is for a TLS proxy in
	// front of it.
	DefaultCDCServerScheme = SchemeHTTP
	// DefaultCDCNetworkMode probes rather than assumes: the same config runs on
	// Docker Desktop and on a native Linux daemon.
	DefaultCDCNetworkMode = NetworkModeAuto
)

// The schemes backends.cdc.server_scheme accepts.
const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// The modes backends.cdc.network_mode accepts: how a restic container reaches
// arcave's restic server.
const (
	// NetworkModeAuto probes the daemon at wiring time: a native Linux daemon
	// gets host networking, anything else the bridge and container_host.
	NetworkModeAuto = "auto"
	// NetworkModeHost joins the containers to the host's network namespace, so
	// the server's loopback bind is reachable as 127.0.0.1 (Linux daemons only).
	NetworkModeHost = "host"
	// NetworkModeBridge keeps the default network and reaches the host by
	// container_host — the escape hatch for remote daemons and rootless setups.
	NetworkModeBridge = "bridge"
)

// The bounds arcave gives compose when it stops and starts the compose project. They live
// here rather than in the compose client because they are project config now:
// the client applies them, but the user names them, and cmd/arcave advertises
// them as the --wait-timeout/--stop-timeout defaults.
const (
	// DefaultWaitTimeout bounds the health-check wait after a start. Without a
	// bound a service that never becomes healthy (a crash-looping container with
	// restart: always) would block a savepoint forever.
	DefaultWaitTimeout = 120 * time.Second
	// DefaultStopTimeout is the least grace period before compose SIGKILLs a
	// container; a service's stop_grace_period raises it. It matters more here than
	// it does to `docker compose stop`: arcave snapshots the volumes the moment the
	// containers are down, so a database killed mid-shutdown is captured mid-shutdown.
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

// DefaultSafetySnapshotRetainRecent is how many safety snapshots a gc leaves each compose project.
const DefaultSafetySnapshotRetainRecent = 1

// DefaultConcurrency is how many volumes, writable layers or images one workflow
// moves at once.
const DefaultConcurrency = 5

// The tokens rollback.leftover_volumes accepts.
const (
	// LeftoverVolumesOverwrite restores over a volume that outlived its compose project's
	// containers, and warns. The default.
	LeftoverVolumesOverwrite = "overwrite"
	// LeftoverVolumesRefuse fails such a rollback before anything is created.
	LeftoverVolumesRefuse = "refuse"
)

// Config is the content persisted to .arcave/config.yaml by init. The yaml tags
// drive Save; the mapstructure tags drive Unmarshal.
// One section per subsystem: a new subsystem adds a section rather than another
// top-level key.
type Config struct {
	Project        Project        `yaml:"project" mapstructure:"project"`
	Common         Common         `yaml:"common" mapstructure:"common"`
	Docker         Docker         `yaml:"docker,omitempty" mapstructure:"docker"`
	Compose        Compose        `yaml:"compose" mapstructure:"compose"`
	Storage        Storage        `yaml:"storage" mapstructure:"storage"`
	Encryption     Encryption     `yaml:"encryption,omitempty" mapstructure:"encryption"`
	Snapshot       Snapshot       `yaml:"snapshot" mapstructure:"snapshot"`
	WritableLayers WritableLayers `yaml:"writable_layers" mapstructure:"writable_layers"`
	Capture        Capture        `yaml:"capture" mapstructure:"capture"`
	Rollback       Rollback       `yaml:"rollback" mapstructure:"rollback"`
	GC             GC             `yaml:"gc" mapstructure:"gc"`
	// Backends holds the settings of the individual snapshot backends, one
	// section per backend, present only for the backends a project configures.
	Backends Backends `yaml:"backends,omitempty" mapstructure:"backends"`
	// Log holds the project's default logging settings, honoured by every
	// command unless overridden by the --log-level/--log-format global flags.
	Log Log `yaml:"log,omitempty" mapstructure:"log"`
}

// Project names the storage tenant every store root is scoped by. The compose
// compose project is not here: it is named per invocation with -p/--project-name.
type Project struct {
	Name string `yaml:"name" mapstructure:"name"`
}

// Validate gates the name, which becomes the tenant segment of every store
// root. ApplyDefaults has run by then, so an empty name means the project
// directory normalized to nothing and the user has to say what to call it.
func (p Project) Validate() error {
	if err := model.ArcaveProjectName(p.Name).Validate(); err != nil {
		return fmt.Errorf("%w: project.name: %w (set project.name explicitly)", ErrInvalid, err)
	}
	return nil
}

// Encryption encrypts every object arcave writes to the store, to Key. A missing or
// malformed Key is refused where the store opens, not here: init writes Enabled with no
// Key, and a Key in the environment reaches a plain project too, where it is ignored.
type Encryption struct {
	Enabled bool `yaml:"enabled,omitempty" mapstructure:"enabled"`
	// Key is the age identity every object is encrypted to. Secret.
	Key string `yaml:"key,omitempty" mapstructure:"key"`
}

// Common holds the settings every workflow shares.
type Common struct {
	// Concurrency caps how many volumes, writable layers or images a capture or a
	// restore works at once.
	Concurrency int `yaml:"concurrency" mapstructure:"concurrency"`
}

// Validate rejects a concurrency no workflow could run with.
func (c Common) Validate() error {
	if c.Concurrency < 1 {
		return fmt.Errorf("%w: common.concurrency must be at least 1, got %d", ErrInvalid, c.Concurrency)
	}
	return nil
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
	CertPath string `yaml:"cert_path,omitempty" mapstructure:"cert_path"`
	CAFile   string `yaml:"ca_file,omitempty" mapstructure:"ca_file"`
	CertFile string `yaml:"cert_file,omitempty" mapstructure:"cert_file"`
	KeyFile  string `yaml:"key_file,omitempty" mapstructure:"key_file"`
	// SkipTLSVerify turns TLS on and accepts any certificate the daemon presents.
	SkipTLSVerify bool `yaml:"skip_tls_verify,omitempty" mapstructure:"skip_tls_verify"`
}

// Compose is how arcave drives the project's lifecycle — the two bounds it gives
// `docker compose stop`/`up`, and how loudly compose reports its own progress.
// Unlike Docker, which says *which* daemon to reach, this section says how
// patient arcave is with the compose project once it has it.
//
// It is a section of its own rather than fields on Project because Project
// identifies the compose project (its name, its files) while these are the knobs a user
// turns when a service is slow: a savepoint that reports nothing for two minutes
// and a database killed mid-shutdown are both fixed from here.
type Compose struct {
	// WaitTimeout bounds the health-check wait after a start. Every command that
	// restarts the compose project advertises it as --wait-timeout; a request naming its own
	// bound (an MCP tool call) overrides it.
	WaitTimeout time.Duration `yaml:"wait_timeout" mapstructure:"wait_timeout"`
	// StopTimeout is the least time a container gets to exit on its own before
	// compose sends SIGKILL; a service's stop_grace_period raises it. Zero means
	// "unset": DefaultStopTimeout applies, so an immediate kill is spelled 1s, not 0 —
	// the value that reads as "no grace at all" is the one a user is most likely to
	// leave unset by accident, and a SIGKILLed database is exactly what this bound
	// exists to prevent.
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
		return fmt.Errorf("%w: compose.wait_timeout must be a positive duration, got %s", ErrInvalid, c.WaitTimeout)
	}
	if c.StopTimeout < 0 {
		return fmt.Errorf("%w: compose.stop_timeout must not be negative, got %s", ErrInvalid, c.StopTimeout)
	}
	switch c.Progress {
	case ProgressLog, ProgressTTY, ProgressQuiet:
		return nil
	default:
		return fmt.Errorf("%w: unsupported compose.progress %q (supported: %s, %s, %s)", ErrInvalid,
			c.Progress, ProgressLog, ProgressTTY, ProgressQuiet)
	}
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
		return fmt.Errorf("%w: unsupported snapshot.backend %q (supported: %s, %s)", ErrInvalid,
			s.Backend, SnapshotBackendFullCopy, SnapshotBackendCDC)
	}
}

// BackendKind translates the configured token into the kind a volume carries —
// the one place the two vocabularies meet. An unknown token yields
// SnapshotBackendUnknown, which the registry refuses by name; Validate has
// rejected it long before.
func (s Snapshot) BackendKind() model.SnapshotBackendKind {
	kind, _ := model.ParseVolumeBackend(s.Backend)
	return kind
}

// WritableLayers selects the layout a new savepoint stores writable layers in, unless a
// service's arcave.writable-layer.backend label says otherwise.
type WritableLayers struct {
	Backend string `yaml:"backend" mapstructure:"backend"`
}

// Validate rejects a layout arcave does not know.
func (w WritableLayers) Validate() error {
	if _, ok := model.ParseImageBackend(w.Backend); !ok {
		return fmt.Errorf("%w: unsupported writable_layers.backend %q (supported: %s, %s)", ErrInvalid,
			w.Backend, model.ImageBackendDedupCopy, model.ImageBackendFullCopy)
	}
	return nil
}

// BackendKind is the configured layout as the kind a capture carries.
func (w WritableLayers) BackendKind() model.ImageBackendKind {
	kind, _ := model.ParseImageBackend(w.Backend)
	return kind
}

// Capture says, by kind of unit, what a savepoint takes when no arcave.* label says
// otherwise. It names no service or volume: those are the compose file's, by label.
type Capture struct {
	WritableLayers *bool          `yaml:"writable_layers" mapstructure:"writable_layers"`
	Images         *bool          `yaml:"images" mapstructure:"images"`
	Volumes        CaptureVolumes `yaml:"volumes" mapstructure:"volumes"`
}

// CaptureVolumes says which kinds of volume a savepoint takes.
type CaptureVolumes struct {
	Named     *bool `yaml:"named" mapstructure:"named"`
	Bind      *bool `yaml:"bind" mapstructure:"bind"`
	Anonymous *bool `yaml:"anonymous" mapstructure:"anonymous"`
}

// Rollback configures the rollback workflow.
type Rollback struct {
	// LeftoverVolumes is what a rollback does when the compose project has no containers
	// but its volumes still exist: overwrite them, or refuse.
	LeftoverVolumes string `yaml:"leftover_volumes" mapstructure:"leftover_volumes"`
	// SafetySnapshot is whether a rollback captures the live volumes and writable layers first, so
	// it can be undone.
	SafetySnapshot *bool `yaml:"safety_snapshot" mapstructure:"safety_snapshot"`
}

// Validate rejects a leftover-volumes policy arcave does not know.
func (r Rollback) Validate() error {
	switch r.LeftoverVolumes {
	case LeftoverVolumesOverwrite, LeftoverVolumesRefuse:
		return nil
	default:
		return fmt.Errorf("%w: unsupported rollback.leftover_volumes %q (supported: %s, %s)", ErrInvalid,
			r.LeftoverVolumes, LeftoverVolumesOverwrite, LeftoverVolumesRefuse)
	}
}

// Policy returns the configured token as the policy a rollback request carries.
func (r Rollback) Policy() model.LeftoverVolumesPolicy {
	return model.LeftoverVolumesPolicy(r.LeftoverVolumes)
}

// SkipSafetySnapshot reports whether the config turns the safety snapshot off; unset takes one.
func (r Rollback) SkipSafetySnapshot() bool {
	return r.SafetySnapshot != nil && !*r.SafetySnapshot
}

// GC configures what `arcave gc` retains.
type GC struct {
	SafetySnapshots GCSafetySnapshots `yaml:"safety_snapshots" mapstructure:"safety_snapshots"`
}

// GCSafetySnapshots says how many safety snapshots a gc leaves each compose project.
type GCSafetySnapshots struct {
	// RetainRecent is how many of the newest to keep; 0 deletes every one.
	RetainRecent *int `yaml:"retain_recent" mapstructure:"retain_recent"`
}

// Validate rejects a negative retention.
func (g GC) Validate() error {
	if retainRecent := utils.Value(g.SafetySnapshots.RetainRecent); retainRecent < 0 {
		return fmt.Errorf("%w: gc.safety_snapshots.retain_recent must not be negative, got %d", ErrInvalid, retainRecent)
	}
	return nil
}

// Backends is one section per snapshot backend. The cdc arm is a pointer so a
// project that never names cdc carries no section for it; fullcopy is one key
// with a default, and every project carries it.
type Backends struct {
	FullCopy FullCopyBackend `yaml:"fullcopy" mapstructure:"fullcopy"`
	CDC      *CDCBackend     `yaml:"cdc,omitempty" mapstructure:"cdc"`
}

// FullCopyBackend configures the wholesale backend. Compression reaches only new
// captures: a restore reads the format off the record.
type FullCopyBackend struct {
	// Compression is zstd (the default) or none.
	Compression string `yaml:"compression" mapstructure:"compression"`
}

// Validate rejects a format the backend cannot write.
func (f FullCopyBackend) Validate() error {
	switch f.Compression {
	case CompressionZstd, CompressionNone:
		return nil
	default:
		return fmt.Errorf("%w: unsupported backends.fullcopy.compression %q (supported: %s, %s)", ErrInvalid,
			f.Compression, CompressionZstd, CompressionNone)
	}
}

// CDCBackend configures the content-defined-chunking backend: which restic image
// its one-shot containers run, how a restore reaches the live mount, and how a
// container reaches its repository — an exec stream, or a loopback TCP hop.
type CDCBackend struct {
	// RestoreMode is wipe (clear the mount, then restore it whole) or incremental
	// (restore over the live data, letting restic write only the difference).
	RestoreMode string `yaml:"restore_mode" mapstructure:"restore_mode"`
	// Overwrite governs an incremental restore only.
	Overwrite string `yaml:"overwrite" mapstructure:"overwrite"`
	// Image is the restic image every cdc command runs in.
	Image string `yaml:"image" mapstructure:"image"`
	// Transport is how a restic container reaches its repository: stdio (an exec
	// stream, the default) or tcp. The four keys below are acted on, and validated,
	// only under tcp.
	Transport string `yaml:"transport" mapstructure:"transport"`
	// ServerAddress is what arcave's restic server binds. Port 0 lets the kernel
	// pick, which is what keeps two arcave processes from colliding.
	ServerAddress string `yaml:"server_address" mapstructure:"server_address"`
	// ServerScheme is the scheme of the repository URL the container fetches
	// over: http (the default; the server itself speaks nothing else) or https,
	// for a TLS proxy fronting it.
	ServerScheme string `yaml:"server_scheme" mapstructure:"server_scheme"`
	// ContainerHost is that server's host name *as the container sees it* on the
	// bridge path: a bridged container cannot reach 127.0.0.1. Ignored under
	// host networking.
	ContainerHost string `yaml:"container_host" mapstructure:"container_host"`
	// NetworkMode is how a restic container reaches the server: auto (probe the
	// daemon), host, or bridge.
	NetworkMode string `yaml:"network_mode" mapstructure:"network_mode"`
}

// Validate checks the arms that are present. An absent cdc arm is not an error: a
// project that does not use cdc does not have to configure it.
func (b Backends) Validate() error {
	if err := b.FullCopy.Validate(); err != nil {
		return err
	}
	if b.CDC == nil {
		return nil
	}
	return b.CDC.Validate()
}

// Validate rejects a mode or an overwrite policy the backend cannot act on, and,
// under tcp, a server it cannot bind or reach.
func (c CDCBackend) Validate() error {
	switch c.RestoreMode {
	case RestoreModeWipe, RestoreModeIncremental:
	default:
		return fmt.Errorf("%w: unsupported backends.cdc.restore_mode %q (supported: %s, %s)", ErrInvalid,
			c.RestoreMode, RestoreModeWipe, RestoreModeIncremental)
	}
	switch c.Overwrite {
	case OverwriteIfChanged, OverwriteAlways:
	default:
		return fmt.Errorf("%w: unsupported backends.cdc.overwrite %q (supported: %s, %s)", ErrInvalid,
			c.Overwrite, OverwriteIfChanged, OverwriteAlways)
	}
	if c.Image == "" {
		return fmt.Errorf("%w: backends.cdc.image is required", ErrInvalid)
	}
	switch c.Transport {
	case CDCTransportStdio:
		return nil
	case CDCTransportTCP:
		return c.validateTCP()
	default:
		return fmt.Errorf("%w: unsupported backends.cdc.transport %q (supported: %s, %s)", ErrInvalid,
			c.Transport, CDCTransportStdio, CDCTransportTCP)
	}
}

// validateTCP checks the keys only the tcp transport reads.
func (c CDCBackend) validateTCP() error {
	if c.ServerAddress == "" {
		return fmt.Errorf("%w: backends.cdc.server_address is required", ErrInvalid)
	}
	// The address is bound, so it must split into host and port here rather than
	// at listen time, halfway through wiring a command.
	if _, _, err := net.SplitHostPort(c.ServerAddress); err != nil {
		return fmt.Errorf("%w: backends.cdc.server_address %q is not a host:port address: %w", ErrInvalid, c.ServerAddress, err)
	}
	switch c.ServerScheme {
	case SchemeHTTP, SchemeHTTPS:
	default:
		return fmt.Errorf("%w: unsupported backends.cdc.server_scheme %q (supported: %s, %s)", ErrInvalid,
			c.ServerScheme, SchemeHTTP, SchemeHTTPS)
	}
	if c.ContainerHost == "" {
		return fmt.Errorf("%w: backends.cdc.container_host is required", ErrInvalid)
	}
	switch c.NetworkMode {
	case NetworkModeAuto, NetworkModeHost, NetworkModeBridge:
	default:
		return fmt.Errorf("%w: unsupported backends.cdc.network_mode %q (supported: %s, %s, %s)", ErrInvalid,
			c.NetworkMode, NetworkModeAuto, NetworkModeHost, NetworkModeBridge)
	}
	return nil
}

// IgnoredTCPKeys names the keys set that only the tcp transport reads, under any other.
func (c CDCBackend) IgnoredTCPKeys() []string {
	if c.Transport == CDCTransportTCP {
		return nil
	}
	var keys []string
	for _, key := range []struct {
		name  string
		value string
	}{
		{
			name:  "backends.cdc.server_address",
			value: c.ServerAddress,
		},
		{
			name:  "backends.cdc.server_scheme",
			value: c.ServerScheme,
		},
		{
			name:  "backends.cdc.container_host",
			value: c.ContainerHost,
		},
		{
			name:  "backends.cdc.network_mode",
			value: c.NetworkMode,
		},
	} {
		if key.value != "" {
			keys = append(keys, key.name)
		}
	}
	return keys
}

// Log is the project's default logging configuration, persisted under the "log"
// key in .arcave/config.yaml.
type Log struct {
	// Level is empty unless set: the output then picks its own, warn above the live tree, else info.
	Level  string `yaml:"level,omitempty" mapstructure:"level"`
	Format string `yaml:"format,omitempty" mapstructure:"format"`
}

// NewConfig returns a Config seeded with the static defaults.
func NewConfig() Config {
	return Config{
		Log: Log{
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
	if c.Project.Name == "" {
		// The name becomes a store-root segment, so the directory's own spelling is
		// normalized rather than taken verbatim: MyApp yields myapp. A basename that
		// normalizes to nothing stays empty and is reported by Validate, which is the
		// one place that says "name the project explicitly".
		c.Project.Name = model.NormalizeArcaveProjectName(filepath.Base(projectRoot)).String()
	}
	if c.Common.Concurrency == 0 {
		c.Common.Concurrency = DefaultConcurrency
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
	c.Storage = c.Storage.withDefaults()
	if c.Snapshot.Backend == "" {
		c.Snapshot.Backend = SnapshotBackendFullCopy
	}
	if c.WritableLayers.Backend == "" {
		c.WritableLayers.Backend = string(model.ImageBackendDedupCopy)
	}
	if c.Capture.WritableLayers == nil {
		c.Capture.WritableLayers = utils.New(true)
	}
	if c.Capture.Images == nil {
		c.Capture.Images = utils.New(true)
	}
	if c.Capture.Volumes.Named == nil {
		c.Capture.Volumes.Named = utils.New(true)
	}
	if c.Capture.Volumes.Bind == nil {
		c.Capture.Volumes.Bind = utils.New(true)
	}
	if c.Capture.Volumes.Anonymous == nil {
		c.Capture.Volumes.Anonymous = utils.New(true)
	}
	if c.Rollback.LeftoverVolumes == "" {
		c.Rollback.LeftoverVolumes = LeftoverVolumesOverwrite
	}
	if c.Rollback.SafetySnapshot == nil {
		c.Rollback.SafetySnapshot = utils.New(true)
	}
	if c.GC.SafetySnapshots.RetainRecent == nil {
		c.GC.SafetySnapshots.RetainRecent = utils.New(DefaultSafetySnapshotRetainRecent)
	}
	// Whatever snapshot.backend says: a label may route a volume through fullcopy.
	if c.Backends.FullCopy.Compression == "" {
		c.Backends.FullCopy.Compression = CompressionZstd
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
	cdc := CDCWithDefaults(c.Backends.CDC)
	c.Backends.CDC = &cdc
}

// CDCWithDefaults resolves the backends.cdc section into the values arcave acts
// on, absent section included — a rollback restores through the backend its
// record names, so cdc has to be buildable even where snapshot.backend is
// fullcopy and the project carries no block. It is the single home of these
// defaults: ApplyDefaults fills the config through it, and the DI root reads the
// effective section through it, so neither side can hold a copy that drifts.
func CDCWithDefaults(configured *CDCBackend) CDCBackend {
	cdc := CDCBackend{}
	if configured != nil {
		cdc = *configured
	}
	if cdc.RestoreMode == "" {
		cdc.RestoreMode = RestoreModeIncremental
	}
	if cdc.Overwrite == "" {
		cdc.Overwrite = OverwriteAlways
	}
	if cdc.Image == "" {
		cdc.Image = DefaultResticImage
	}
	if cdc.Transport == "" {
		cdc.Transport = DefaultCDCTransport
	}
	if cdc.Transport != CDCTransportTCP {
		return cdc
	}
	// The keys only tcp reads default only under it.
	if cdc.ServerAddress == "" {
		cdc.ServerAddress = DefaultCDCServerAddress
	}
	if cdc.ServerScheme == "" {
		cdc.ServerScheme = DefaultCDCServerScheme
	}
	if cdc.ContainerHost == "" {
		cdc.ContainerHost = DefaultCDCContainerHost
	}
	if cdc.NetworkMode == "" {
		cdc.NetworkMode = DefaultCDCNetworkMode
	}
	return cdc
}

// Validate checks the sections whose values arcave must be able to act on. It runs
// after ApplyDefaults, so an empty section is a default rather than an error.
func (c *Config) Validate() error {
	if err := c.Project.Validate(); err != nil {
		return err
	}
	if err := c.Common.Validate(); err != nil {
		return err
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
	if err := c.WritableLayers.Validate(); err != nil {
		return err
	}
	if err := c.Rollback.Validate(); err != nil {
		return err
	}
	if err := c.GC.Validate(); err != nil {
		return err
	}
	return c.Backends.Validate()
}

// Path returns the absolute config file path for the project rooted at
// projectRoot.
func Path(projectRoot string) string {
	return filepath.Join(projectRoot, model.StateDir, fileName)
}
