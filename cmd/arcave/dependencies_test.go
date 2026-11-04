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

package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/docker/cli/cli/flags"
	composeprogress "github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/client"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/storekey"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// newCDCOptions is the translation that keeps internal/config free of any package
// carrying a client: the config's own vocabulary in, the backend's own out.
func TestNewCDCOptions(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.CDCBackend
		password string
		want     cdc.Options
	}{
		{
			// A project that never names cdc carries no section, and still gets a
			// runnable image: an old cdc savepoint is rolled back through cdc
			// whatever snapshot.backend says now.
			name:     "an absent section resolves to the defaults",
			cfg:      config.CDCWithDefaults(nil),
			password: cdc.DefaultPassword,
			want: cdc.Options{
				Image:       config.DefaultResticImage,
				Overwrite:   config.OverwriteAlways,
				Incremental: true,
				Password:    cdc.DefaultPassword,
			},
		},
		{
			name: "wipe is not incremental, whatever overwrite says",
			cfg: config.CDCBackend{
				RestoreMode: config.RestoreModeWipe,
				Overwrite:   config.OverwriteAlways,
				Image:       config.DefaultResticImage,
			},
			want: cdc.Options{
				Image:     config.DefaultResticImage,
				Overwrite: config.OverwriteAlways,
			},
		},
		{
			name: "incremental carries its overwrite policy through",
			cfg: config.CDCBackend{
				RestoreMode: config.RestoreModeIncremental,
				Overwrite:   config.OverwriteIfChanged,
				Image:       "restic/restic:0.18.0",
			},
			password: "derived",
			want: cdc.Options{
				Image:       "restic/restic:0.18.0",
				Incremental: true,
				Overwrite:   config.OverwriteIfChanged,
				Password:    "derived",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newCDCOptions(tt.cfg, tt.password)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// newDockerClientOptions is what carries the config into docker/cli. An unset option
// must not be passed through: an empty host would override the environment's
// DOCKER_HOST rather than defer to it.
func TestNewDockerClientOptions(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Docker
		wantErr string
		verify  func(t *testing.T, got *flags.ClientOptions)
	}{
		{
			name: "the zero config passes nothing through",
			cfg:  config.Docker{},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.Empty(t, got.Hosts)
				assert.Empty(t, got.Context)
				assert.False(t, got.TLS)
				assert.Nil(t, got.TLSOptions)
			},
		},
		{
			name: "a host",
			cfg:  config.Docker{Host: "tcp://10.0.0.1:2376"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.Equal(t, []string{"tcp://10.0.0.1:2376"}, got.Hosts)
			},
		},
		{
			name: "tls settings expand to the three pem paths",
			cfg:  config.Docker{Host: "tcp://10.0.0.1:2376", CertPath: "/certs", TLSVerify: true},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				assert.True(t, got.TLSVerify)
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/certs/ca.pem", got.TLSOptions.CAFile)
				assert.Equal(t, "/certs/cert.pem", got.TLSOptions.CertFile)
				assert.Equal(t, "/certs/key.pem", got.TLSOptions.KeyFile)
			},
		},
		{
			name: "explicit files override the cert path per file",
			cfg:  config.Docker{CertPath: "/certs", CAFile: "/etc/ca.pem", KeyFile: "/etc/key.pem", TLSVerify: true},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/etc/ca.pem", got.TLSOptions.CAFile, "explicit CAFile wins")
				assert.Equal(t, "/certs/cert.pem", got.TLSOptions.CertFile, "unset file falls back to the cert path")
				assert.Equal(t, "/etc/key.pem", got.TLSOptions.KeyFile, "explicit KeyFile wins")
			},
		},
		{
			name: "explicit files alone enable tls without a cert path",
			cfg:  config.Docker{CAFile: "/etc/ca.pem", CertFile: "/etc/cert.pem", KeyFile: "/etc/key.pem"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/etc/ca.pem", got.TLSOptions.CAFile)
				assert.Equal(t, "/etc/cert.pem", got.TLSOptions.CertFile)
				assert.Equal(t, "/etc/key.pem", got.TLSOptions.KeyFile)
			},
		},
		{
			name: "tls verify off carries no tls config",
			cfg:  config.Docker{TLSVerify: false},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.False(t, got.TLS)
				assert.Nil(t, got.TLSOptions)
			},
		},
		{
			// Cert material is enough to turn TLS on: a caller who names a cert path
			// wants an encrypted connection even if they never asked to verify the
			// daemon's certificate, so TLS is enabled and only TLSVerify stays off.
			name: "a cert path alone enables tls with verify off",
			cfg:  config.Docker{CertPath: "/certs"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS, "cert material enables TLS")
				assert.False(t, got.TLSVerify, "verify stays off unless asked for")
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/certs/ca.pem", got.TLSOptions.CAFile)
				assert.Equal(t, "/certs/cert.pem", got.TLSOptions.CertFile)
				assert.Equal(t, "/certs/key.pem", got.TLSOptions.KeyFile)
			},
		},
		{
			// The rule this replaced was the inverse: the raw SDK client could not
			// resolve a context, so one was refused without a host. docker/cli can.
			name: "a context alone is enough",
			cfg:  config.Docker{Context: "remote"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.Equal(t, "remote", got.Context)
				assert.Empty(t, got.Hosts)
			},
		},
		{
			// tls_verify with no cert material is the public-CA case: verify the
			// daemon against the system roots, present no client certificate.
			name: "tls verify alone enables tls with no cert material",
			cfg:  config.Docker{TLSVerify: true},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				assert.True(t, got.TLSVerify)
				require.NotNil(t, got.TLSOptions)
				assert.Empty(t, got.TLSOptions.CAFile)
				assert.Empty(t, got.TLSOptions.CertFile)
				assert.Empty(t, got.TLSOptions.KeyFile)
			},
		},
		{
			// A CA to verify against, but no client certificate — still a complete
			// config, because cert/key are a pair that is simply absent here.
			name: "a ca file alone verifies without a client certificate",
			cfg:  config.Docker{CAFile: "/etc/ca.pem", TLSVerify: true},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/etc/ca.pem", got.TLSOptions.CAFile)
				assert.Empty(t, got.TLSOptions.CertFile)
				assert.Empty(t, got.TLSOptions.KeyFile)
			},
		},
		{
			name:    "a client cert without its key is refused",
			cfg:     config.Docker{CertFile: "/etc/cert.pem"},
			wantErr: "docker.key_file is missing",
		},
		{
			name:    "a client key without its cert is refused",
			cfg:     config.Docker{KeyFile: "/etc/key.pem"},
			wantErr: "docker.cert_file is missing",
		},
		{
			// cert_path supplies the whole pair from one directory, so half a pair
			// spelled out explicitly is still completed by it rather than refused.
			name: "a cert path completes a half-specified pair",
			cfg:  config.Docker{CertPath: "/certs", CertFile: "/etc/cert.pem"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/etc/cert.pem", got.TLSOptions.CertFile, "explicit CertFile wins")
				assert.Equal(t, "/certs/key.pem", got.TLSOptions.KeyFile, "the missing key falls back to the cert path")
			},
		},
		{
			name:    "a host and a context together name two daemons",
			cfg:     config.Docker{Host: "unix:///d.sock", Context: "remote"},
			wantErr: "mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := newDockerClientOptions(tt.cfg)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.verify(t, got)
		})
	}
}

// newDockerTLSOptions honours docker's two-part TLS model: a cert_file/key_file pair
// (mutual TLS) that must be supplied together, and ca_file/tls_verify governing
// server verification independently. The useTLS return is what keeps an all-empty
// config off TLS entirely, so it is asserted alongside the resolved paths.
func TestNewDockerTLSOptions(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Docker
		wantErr     string
		wantUseTLS  bool
		wantCAFile  string
		wantCert    string
		wantKeyFile string
	}{
		{
			name:       "an empty config leaves tls off",
			cfg:        config.Docker{},
			wantUseTLS: false,
		},
		{
			name:       "tls verify alone turns tls on with no cert material",
			cfg:        config.Docker{TLSVerify: true},
			wantUseTLS: true,
		},
		{
			name:       "a ca file alone verifies without a client certificate",
			cfg:        config.Docker{CAFile: "/etc/ca.pem"},
			wantUseTLS: true,
			wantCAFile: "/etc/ca.pem",
		},
		{
			name:        "a full pair enables tls",
			cfg:         config.Docker{CertFile: "/etc/cert.pem", KeyFile: "/etc/key.pem"},
			wantUseTLS:  true,
			wantCert:    "/etc/cert.pem",
			wantKeyFile: "/etc/key.pem",
		},
		{
			name:        "a cert path expands to the three pem paths",
			cfg:         config.Docker{CertPath: "/certs"},
			wantUseTLS:  true,
			wantCAFile:  "/certs/ca.pem",
			wantCert:    "/certs/cert.pem",
			wantKeyFile: "/certs/key.pem",
		},
		{
			name:        "a cert path completes a half-specified pair",
			cfg:         config.Docker{CertPath: "/certs", KeyFile: "/etc/key.pem"},
			wantUseTLS:  true,
			wantCAFile:  "/certs/ca.pem",
			wantCert:    "/certs/cert.pem",
			wantKeyFile: "/etc/key.pem",
		},
		{
			name:    "a client cert without its key is refused",
			cfg:     config.Docker{CertFile: "/etc/cert.pem"},
			wantErr: "docker.key_file is missing",
		},
		{
			name:    "a client key without its cert is refused",
			cfg:     config.Docker{KeyFile: "/etc/key.pem"},
			wantErr: "docker.cert_file is missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			opts, useTLS, err := newDockerTLSOptions(tt.cfg)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.False(t, useTLS, "a refused config must not enable TLS")
				assert.Nil(t, opts)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUseTLS, useTLS)
			if !tt.wantUseTLS {
				assert.Nil(t, opts)
				return
			}
			require.NotNil(t, opts)
			assert.Equal(t, tt.wantCAFile, opts.CAFile)
			assert.Equal(t, tt.wantCert, opts.CertFile)
			assert.Equal(t, tt.wantKeyFile, opts.KeyFile)
		})
	}
}

// Compose's progress is what turns a two-minute health-check wait from a hang
// into something a user can watch, so every channel must reach the library — and
// none of them may reach stdout, which carries arcave's JSON.
func TestNewProgressWriter(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		wantMode string
		wantOut  io.Writer // nil means "the adapter", which has no comparable value
	}{
		{
			name: "log folds compose's events into arcave's own log",
			mode: config.ProgressLog, wantMode: composeprogress.ModeJSON,
		},
		{
			// ModeAuto, not ModeTTY: compose then redraws on a terminal and falls
			// back to plain lines in a pipe, where ModeTTY would spray ANSI escapes
			// into a log file.
			name: "tty hands the terminal back to compose's own renderer",
			mode: config.ProgressTTY, wantMode: composeprogress.ModeAuto, wantOut: os.Stderr,
		},
		{
			name: "quiet discards the stream as well as the UI",
			mode: config.ProgressQuiet, wantMode: composeprogress.ModeQuiet, wantOut: io.Discard,
		},
		{
			name: "an unset channel is the log",
			mode: "", wantMode: composeprogress.ModeJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: progress.Mode is the library's global, so it is restored.
			original := composeprogress.Mode
			t.Cleanup(func() { composeprogress.Mode = original })

			// Act
			out, events := newProgressWriter(tt.mode, zerolog.Nop())

			// Assert
			assert.Equal(t, tt.wantMode, composeprogress.Mode)
			if tt.wantOut == nil {
				assert.IsType(t, &compose.ProgressLogger{}, out)
				assert.Same(t, out, events, "the adapter compose renders on is the one its events reach")
				return
			}
			assert.Nil(t, events)
			// The streams arcave does not own are wrapped so Close cannot reach
			// them — closing os.Stderr would take the process's error stream down
			// with it.
			assert.Equal(t, nopWriteCloser{tt.wantOut}, out)
			assert.NoError(t, out.Close())
		})
	}
}

// A host resolves to an endpoint whether or not anything is listening on it:
// arcave savepoint list has to work with docker stopped.
func TestNewDockerClient_AcceptsAnUnreachableHost(t *testing.T) {
	// Arrange: an empty config dir, so the developer's own docker config cannot
	// steer the resolution.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	// Act
	dockerCli, apiClient, err := newDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.NotNil(t, dockerCli)
	assert.NotNil(t, apiClient)
}

// api_version is the one docker field carried not through ClientOptions but
// through the environment: docker/cli reads DOCKER_API_VERSION and otherwise
// negotiates. arcave spawns no child processes, so setting it configures arcave's own
// client and nothing else.
func TestNewDockerClient_PinsAPIVersion(t *testing.T) {
	// Arrange: an empty config dir, and a known-empty env var t restores afterwards.
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv(client.EnvOverrideAPIVersion, "")

	// Act
	_, apiClient, err := newDockerClient(config.Docker{
		Host:       "unix:///nowhere/docker.sock",
		APIVersion: "1.45",
	}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.Equal(t, "1.45", os.Getenv(client.EnvOverrideAPIVersion),
		"cfg.APIVersion must reach docker/cli through DOCKER_API_VERSION")
}

// An unset api_version leaves DOCKER_API_VERSION untouched, so docker/cli
// negotiates rather than being pinned to a stale value the caller never asked
// for — the empty field is "read the environment", not "clear it".
func TestNewDockerClient_UnsetAPIVersionLeavesTheEnvUntouched(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv(client.EnvOverrideAPIVersion, "1.40")

	// Act
	_, apiClient, err := newDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.Equal(t, "1.40", os.Getenv(client.EnvOverrideAPIVersion),
		"an unset APIVersion must not disturb a version already in the environment")
}

// A context that does not exist must surface as an error. docker/cli's own
// answer is to print to the (discarded) error stream and os.Exit(1), which would
// kill arcave with no output at all — so the endpoint is resolved up front, where a
// failure is still returnable.
func TestNewDockerClient_RejectsAnUnknownContext(t *testing.T) {
	// Arrange: an empty config dir, so no context store can define the name.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	// Act
	_, _, err := newDockerClient(config.Docker{Context: "nosuchcontext"}, io.Discard)

	// Assert
	require.ErrorContains(t, err, "nosuchcontext")
}

// The other half of the os.Exit(1) surface, and the one a check on the resolved
// endpoint's Host misses: here the endpoint resolves fine and it is building a
// client from it that fails, so docker/cli holds the failure behind a perfectly
// good Host and only answers on the next Client() call — by exiting. Building
// the client up front is what turns it into an error.
func TestNewDockerClient_RejectsAnUnbuildableClient(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_CUSTOM_HEADERS", "not-a-key-value-pair")

	// Act
	_, _, err := newDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.ErrorContains(t, err, "DOCKER_CUSTOM_HEADERS")
}

// The config's capture keys reach the policy as written; one left unset is captured,
// because ApplyDefaults sets it.
func TestNewCaptureDefaults(t *testing.T) {
	// Arrange
	cfg := config.Config{
		Snapshot:       config.Snapshot{Backend: config.SnapshotBackendCDC},
		WritableLayers: config.WritableLayers{Backend: "fullcopy"},
		Capture: config.Capture{
			WritableLayers: utils.New(false),
			Volumes:        config.CaptureVolumes{Bind: utils.New(false), Named: utils.New(true)},
		},
	}
	cfg.ApplyDefaults(t.TempDir())

	// Act
	got := newCaptureDefaults(cfg)

	// Assert
	assert.Equal(t, capturepolicy.CaptureDefaults{
		VolumeBackend:        model.SnapshotBackendCDC,
		WritableLayerBackend: model.ImageBackendFullCopy,
		SavepointCapture: capturepolicy.SavepointCaptureSettings{
			WritableLayers:   false,
			Images:           true,
			NamedVolumes:     true,
			BindVolumes:      false,
			AnonymousVolumes: true,
		},
	}, got)
}

// A gc connects to a daemon that does not answer, and the restored-layer collector's probe says so:
// the gc decides whether to leave the kind out or abort it.
func TestNewDaemonCollectors_DaemonDoesNotAnswer(t *testing.T) {
	// Arrange
	_, dockerClientAPI, err := newDockerClient(config.Docker{Host: "tcp://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)
	engine := docker.NewEngine(dockerClientAPI)
	t.Cleanup(func() { _ = engine.Close() })
	collectors := newDaemonCollectors(config.Config{}, engine)

	// Act
	var probeErr error
	for _, collector := range collectors {
		if probe, ok := collector.(interface{ Probe(context.Context) error }); ok {
			probeErr = probe.Probe(t.Context())
		}
	}

	// Assert
	require.Error(t, probeErr)
}

// Every data kind is resolved by exactly one collector, at the level its report names.
func TestNewObjectCollectors(t *testing.T) {
	// Arrange
	_, dockerClientAPI, err := newDockerClient(config.Docker{Host: "tcp://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)
	engine := docker.NewEngine(dockerClientAPI)
	t.Cleanup(func() { _ = engine.Close() })
	type resolution struct {
		collector model.GarbageCollectorKind
		daemon    bool
	}
	want := map[model.GarbageDataKind]resolution{
		model.GarbageDataKindRestoredLayers:      {collector: model.GarbageCollectorKindDockerLayerTags, daemon: true},
		model.GarbageDataKindSavepointData:       {collector: model.GarbageCollectorKindSavepointDirectory},
		model.GarbageDataKindUpstreamImages:      {collector: model.GarbageCollectorKindOCIImages},
		model.GarbageDataKindImageLayers:         {collector: model.GarbageCollectorKindOCIBlobs},
		model.GarbageDataKindDeduplicatedVolumes: {collector: model.GarbageCollectorKindResticRepositories},
	}

	// Act
	got := map[model.GarbageDataKind][]resolution{}
	for _, collector := range newDaemonCollectors(config.Config{}, engine) {
		got[collector.DataKind()] = append(got[collector.DataKind()], resolution{collector: collector.Kind(), daemon: true})
	}
	for _, collector := range newComposeProjectCollectors(config.Config{}, engine, cdc.DefaultPassword) {
		got[collector.DataKind()] = append(got[collector.DataKind()], resolution{collector: collector.Kind()})
	}

	// Assert
	require.Len(t, want, len(model.GarbageDataKinds()), "the table names every data kind")
	for dataKind, resolved := range want {
		assert.Equal(t, []resolution{resolved}, got[dataKind], dataKind)
	}
	assert.Len(t, got, len(want), "no collector resolves a data kind arcave does not know")
	var collectorKinds []model.GarbageCollectorKind
	for _, resolved := range got {
		for _, resolution := range resolved {
			collectorKinds = append(collectorKinds, resolution.collector)
		}
	}
	assert.ElementsMatch(t, model.GarbageCollectorKinds(), collectorKinds, "every collector kind is built once")
}

// The daemon's one gc action, untag, has its eraser.
func TestNewDaemonErasers(t *testing.T) {
	// Act
	var got []model.GarbageAction
	for _, eraser := range newDaemonErasers(nil) {
		got = append(got, eraser.Action())
	}

	// Assert
	assert.Equal(t, []model.GarbageAction{model.GarbageActionUntag}, got)
}

// Every action a gc deletes in a compose project's store has one eraser.
func TestNewComposeProjectErasers(t *testing.T) {
	// Arrange
	want := []model.GarbageAction{
		model.GarbageActionDeleteTree, model.GarbageActionDeleteImage, model.GarbageActionDeleteObject,
		model.GarbageActionForget,
	}

	// Act
	var got []model.GarbageAction
	for _, eraser := range newComposeProjectErasers(config.Config{}, nil, cdc.DefaultPassword) {
		got = append(got, eraser.Action())
	}

	// Assert
	assert.Equal(t, want, got)
}

// newTestResticInspectorOpener is a tcp opener over a fresh directory store, its containers on
// the host network so that no daemon is asked.
func newTestResticInspectorOpener(t *testing.T) (resticInspectorOpener, storages.Storager) {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	cfg := config.CDCWithDefaults(&config.CDCBackend{
		Transport:   config.CDCTransportTCP,
		NetworkMode: config.NetworkModeHost,
	})
	return resticInspectorOpener{cfg: cfg}, store
}

// stdio serves the repositories over each container's exec stream and listens on nothing, so
// the tcp keys are not read; tcp binds the server and names it to the containers.
func TestOpenCDCTransport(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.CDCBackend
		verify  func(t *testing.T, opened openedCDCTransport)
		wantErr string
	}{
		{
			// auto would ask the nil engine and the address would not bind, so
			// either one read under stdio fails the open.
			name: "stdio is the default and reads no tcp key",
			cfg: config.CDCWithDefaults(&config.CDCBackend{
				NetworkMode:   config.NetworkModeAuto,
				ServerAddress: "192.0.2.1:1",
			}),
			verify: func(t *testing.T, opened openedCDCTransport) {
				transport, ok := opened.transport.(cdc.StdioTransport)
				require.True(t, ok, "transport = %T", opened.transport)
				assert.NotNil(t, transport.Server)
				assert.Nil(t, opened.server, "stdio listens on nothing")
				assert.NotPanics(t, func() { opened.Close(context.Background()) }, "Close has nothing to stop")
			},
		},
		{
			name: "tcp",
			cfg: config.CDCWithDefaults(&config.CDCBackend{
				Transport:   config.CDCTransportTCP,
				NetworkMode: config.NetworkModeHost,
			}),
			verify: func(t *testing.T, opened openedCDCTransport) {
				require.NotNil(t, opened.server)
				port := strconv.Itoa(opened.server.Listener.Addr().(*net.TCPAddr).Port)
				assert.Equal(t, cdc.TCPTransport{
					RepoURLBase: "rest:http://127.0.0.1:" + port,
					HostNetwork: true,
				}, opened.transport)
			},
		},
		{
			name:    "a transport arcave does not offer is refused",
			cfg:     config.CDCBackend{Transport: "ssh"},
			wantErr: `unsupported backends.cdc.transport "ssh"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
			require.NoError(t, err)

			// Act
			opened, err := openCDCTransport(ctx, tt.cfg, nil, store)

			// Assert
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { opened.Close(ctx) })
			tt.verify(t, opened)
		})
	}
}

// The opener serves the compose project's repositories while the inspector is open.
func TestResticInspectorOpener_Open(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	opener, store := newTestResticInspectorOpener(t)

	// Act
	inspector, err := opener.Open(ctx, store)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { inspector.Close(ctx) })
	conn, err := net.Dial("tcp", inspector.(resticInspector).server.Listener.Addr().String())
	require.NoError(t, err, "the server listens")
	assert.NoError(t, conn.Close())
}

// An address the server cannot bind fails the open.
func TestResticInspectorOpener_Open_AddressTaken(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	opener, store := newTestResticInspectorOpener(t)
	opener.cfg.ServerAddress = listener.Addr().String()

	// Act
	_, err = opener.Open(ctx, store)

	// Assert
	assert.ErrorContains(t, err, "start the restic server")
}

// Close stops the server the open started.
func TestResticInspector_Close(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	opener, store := newTestResticInspectorOpener(t)
	inspector, err := opener.Open(ctx, store)
	require.NoError(t, err)
	listener := inspector.(resticInspector).server.Listener

	// Act
	inspector.Close(ctx)

	// Assert: a dial could reach whatever took the freed port since.
	_, err = listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "nothing listens once closed")
}

// The pruner opener serves the compose project's repositories while the pruner is open.
func TestResticPrunerOpener_Open(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	inspectorOpener, store := newTestResticInspectorOpener(t)
	opener := resticPrunerOpener{cfg: inspectorOpener.cfg}

	// Act
	pruner, err := opener.Open(ctx, store)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { pruner.Close(ctx) })
	conn, err := net.Dial("tcp", pruner.(resticPruner).server.Listener.Addr().String())
	require.NoError(t, err, "the server listens")
	assert.NoError(t, conn.Close())
}

// Close stops the server the open started.
func TestResticPruner_Close(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	inspectorOpener, store := newTestResticInspectorOpener(t)
	pruner, err := resticPrunerOpener{cfg: inspectorOpener.cfg}.Open(ctx, store)
	require.NoError(t, err)
	listener := pruner.(resticPruner).server.Listener

	// Act
	pruner.Close(ctx)

	// Assert: a dial could reach whatever took the freed port since.
	_, err = listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "nothing listens once closed")
}

// The served repositories are the compose project store's restic/ namespace, as objects too: a
// pruner clears the locks it finds there.
func TestOpenResticRepositories(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	opener, store := newTestResticInspectorOpener(t)
	require.NoError(t, store.PutObject(ctx, "restic/volume-named-db/locks/a1", strings.NewReader("orphan")))

	// Act
	served, err := openResticRepositories(ctx, opener.cfg, nil, store)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { served.Close(ctx) })
	objects, err := served.root.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, "volume-named-db/locks/a1", objects[0].Name)
	transport, ok := served.transport.(cdc.TCPTransport)
	require.True(t, ok, "transport = %T", served.transport)
	assert.Contains(t, transport.RepoURLBase, strconv.Itoa(served.server.Listener.Addr().(*net.TCPAddr).Port))
}

// eventRecorder keeps every event it is handed.
type eventRecorder struct {
	events []progress.Event
}

func (r *eventRecorder) Render(evt progress.Event) {
	r.events = append(r.events, evt)
}

func TestComposeEventSink_Event(t *testing.T) {
	// Arrange
	recorder := &eventRecorder{}
	ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, time.Now))
	ctx, _ = progress.Begin(ctx, progress.Step{Kind: "savepoint.create"})
	ctx, step := progress.Run(ctx, progress.Step{Kind: "compose.stop"}, progress.MeasureResources)
	sink := newComposeEventSink(ctx)
	require.NotNil(t, sink)

	// Act
	sink.Event(compose.Event{Kind: "Container", Name: "demo-db-1", Status: "Stopping", State: compose.EventWorking})
	sink.Event(compose.Event{Kind: "Container", Name: "demo-db-1", Status: "Stopped", State: compose.EventDone})
	sink.Event(compose.Event{Name: "3f2a", Status: "Error", Text: "denied", State: compose.EventError})
	step.End(nil)

	// Assert
	type line struct {
		status   progress.Status
		msg      string
		resource progress.Resource
		settled  bool
	}
	var got []line
	for _, evt := range recorder.events {
		if evt.Resource != nil {
			got = append(got, line{evt.Status, evt.Msg, *evt.Resource, evt.Settled})
		}
	}
	assert.Equal(t, []line{
		{progress.StatusInProgress, "Stopping", progress.Resource{Kind: "container", ID: "demo-db-1"}, false},
		{progress.StatusInProgress, "Stopped", progress.Resource{Kind: "container", ID: "demo-db-1"}, true},
		{progress.StatusWarned, "Error denied", progress.Resource{Kind: composeResourceObject, ID: "3f2a"}, false},
	}, got)
}

func TestNewComposeEventSink_MuteNodeKeepsTheLog(t *testing.T) {
	// Act
	sink := newComposeEventSink(context.Background())

	// Assert
	assert.Nil(t, sink)
}

// The encryption section decides how an arcave project's store opens again: an encrypted one
// reads back with its key, a plain one plainly, and a section that does not fit the store is
// refused with the fix, and writes nothing — a plain store is never encrypted in place.
func TestOpenArcaveProjectStore(t *testing.T) {
	useFastStoreAdmitter(t)
	key := newTestStoreKey(t)
	encrypted := func(t *testing.T, projectPath string) {
		t.Helper()
		created, err := openArcaveProjectStore(t.Context(),
			newEncryptionConfig(t, projectPath, config.Encryption{Enabled: true, Key: key}), projectPath)
		require.NoError(t, err)
		require.NoError(t, created.store.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("a secret")))
		require.NoError(t, created.root.Close())
	}
	plain := func(t *testing.T, projectPath string) {
		t.Helper()
		created, err := openArcaveProjectStore(t.Context(), newEncryptionConfig(t, projectPath, config.Encryption{}), projectPath)
		require.NoError(t, err)
		require.NoError(t, created.store.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("{}")))
		require.NoError(t, created.root.Close())
	}
	tornMarker := func(t *testing.T, projectPath string) {
		t.Helper()
		marker := filepath.Join(arcaveProjectPath(projectPath), encryptionmarker.FileName)
		require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
		require.NoError(t, os.WriteFile(marker, []byte(`{"version":1,"recip`), 0o644))
	}
	tests := []struct {
		name       string
		stored     func(t *testing.T, projectPath string)
		encryption config.Encryption
		wantErr    error
		wantMsg    string
		verify     func(t *testing.T, projectPath string, opened openedArcaveProject)
	}{
		{
			name:       "an encrypted store with the key it was created with",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true, Key: key},
			verify: func(t *testing.T, projectPath string, opened openedArcaveProject) {
				assert.Equal(t, "a secret", readStoreObject(t, opened.store, "sessions/x/session.json"))
				assert.NotEqual(t, cdc.DefaultPassword, opened.resticPassword)
				raw, err := os.ReadFile(filepath.Join(arcaveProjectPath(projectPath), "sessions", "x", "session.json"))
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "a secret")
			},
		},
		{
			name:       "an encrypted store with another key",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true, Key: newTestStoreKey(t)},
			wantErr:    model.ErrWrongStoreKey,
			wantMsg:    "check encryption.key",
		},
		{
			name:       "an encrypted store with encryption off and the key still exported",
			stored:     encrypted,
			encryption: config.Encryption{Key: key},
			wantErr:    model.ErrStoreEncrypted,
			wantMsg:    "set encryption.enabled: true",
		},
		{
			name:       "an encrypted store with no key",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true},
			wantErr:    model.ErrInvalidStoreKey,
			wantMsg:    "none set (check encryption.key",
		},
		{
			name:       "an encrypted store with a malformed key",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true, Key: "AGE-SECRET-KEY-1NOTAKEY"},
			wantErr:    model.ErrInvalidStoreKey,
			wantMsg:    "check encryption.key",
		},
		{
			name:    "an encrypted store with encryption off",
			stored:  encrypted,
			wantErr: model.ErrStoreEncrypted,
			wantMsg: "set encryption.enabled: true",
		},
		{
			name:   "a plain store with encryption off",
			stored: plain,
			verify: func(t *testing.T, _ string, opened openedArcaveProject) {
				assert.Equal(t, cdc.DefaultPassword, opened.resticPassword)
				assert.Equal(t, "{}", readStoreObject(t, opened.store, "sessions/x/session.json"))
			},
		},
		{
			name:       "a plain store with the key exported and encryption off",
			stored:     plain,
			encryption: config.Encryption{Key: key},
			verify: func(t *testing.T, _ string, opened openedArcaveProject) {
				assert.Equal(t, cdc.DefaultPassword, opened.resticPassword, "the key is ignored")
				assert.Equal(t, "{}", readStoreObject(t, opened.store, "sessions/x/session.json"))
			},
		},
		{
			name:       "a plain store with encryption on",
			stored:     plain,
			encryption: config.Encryption{Enabled: true, Key: key},
			wantErr:    model.ErrStoreUnencrypted,
			wantMsg:    "point storage at an empty location",
		},
		{
			name:       "an empty store under a torn marker is marked again",
			stored:     tornMarker,
			encryption: config.Encryption{Enabled: true, Key: key},
			verify: func(t *testing.T, projectPath string, _ openedArcaveProject) {
				identity, err := storekey.ParseKey(key)
				require.NoError(t, err)
				raw, err := os.ReadFile(filepath.Join(arcaveProjectPath(projectPath), encryptionmarker.FileName))
				require.NoError(t, err)
				assert.Contains(t, string(raw), identity.Recipient().String())
			},
		},
		{
			name:    "a torn marker refuses a plain host",
			stored:  tornMarker,
			wantErr: model.ErrInvalidEncryptionMarker,
			wantMsg: "cut off marking",
		},
		{
			name: "a torn marker over data is named, not read as plain data",
			stored: func(t *testing.T, projectPath string) {
				plain(t, projectPath)
				tornMarker(t, projectPath)
			},
			encryption: config.Encryption{Enabled: true, Key: key},
			wantErr:    model.ErrInvalidEncryptionMarker,
			wantMsg:    "cut off marking",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			projectPath := t.TempDir()
			tt.stored(t, projectPath)
			before := listTree(t, arcaveProjectPath(projectPath))

			// Act
			opened, err := openArcaveProjectStore(t.Context(), newEncryptionConfig(t, projectPath, tt.encryption), projectPath)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMsg)
				if tt.encryption.Key != "" {
					assert.NotContains(t, err.Error(), tt.encryption.Key)
				}
				assert.Equal(t, before, listTree(t, arcaveProjectPath(projectPath)), "a refusal writes nothing")
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, opened.root.Close()) })
			tt.verify(t, projectPath, opened)
		})
	}
}

// arcaveProjectPath is where the directory storage of newEncryptionConfig keeps the demo arcave project.
func arcaveProjectPath(projectPath string) string {
	return filepath.Join(projectPath, ".arcave", "storage", "arcave", "projects", "demo")
}

// listTree is every file under root with its contents, as a refusal must leave it.
func listTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[path] = string(data)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return tree
	}
	require.NoError(t, err)
	return tree
}

func newEncryptionConfig(t *testing.T, projectPath string, encryption config.Encryption) config.Config {
	t.Helper()
	cfg := config.Config{
		Project:    config.Project{Name: "demo"},
		Encryption: encryption,
	}
	cfg.ApplyDefaults(projectPath)
	require.NoError(t, cfg.Validate())
	return cfg
}

// useFastStoreAdmitter admits the store with no settle on the marker's read-back.
func useFastStoreAdmitter(t *testing.T) {
	t.Helper()
	admitter := storeAdmitter
	storeAdmitter = storekey.NewAdmitter().SetSettle(0)
	t.Cleanup(func() { storeAdmitter = admitter })
}

func newTestStoreKey(t *testing.T) string {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return identity.String()
}

func readStoreObject(t *testing.T, store storages.Storager, key string) string {
	t.Helper()
	body, err := store.GetObject(t.Context(), key)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, body.Close()) })
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	return string(data)
}
