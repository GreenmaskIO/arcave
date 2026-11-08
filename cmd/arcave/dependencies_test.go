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
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	clicontext "github.com/docker/cli/cli/context"
	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/client/podman"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// newCDCOptions is the translation that keeps internal/config free of any package
// carrying a client: the config's own vocabulary in, the backend's own out.
func TestNewCDCOptions(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.CDCBackend
		want cdc.Options
	}{
		{
			// A project that never names cdc carries no section, and still gets a
			// runnable image: an old cdc savepoint is rolled back through cdc
			// whatever snapshot.backend says now.
			name: "an absent section resolves to the defaults",
			cfg:  config.CDCWithDefaults(nil),
			want: cdc.Options{
				Image:       config.DefaultResticImage,
				Overwrite:   config.OverwriteAlways,
				Incremental: true,
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
			want: cdc.Options{
				Image:       "restic/restic:0.18.0",
				Incremental: true,
				Overwrite:   config.OverwriteIfChanged,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newCDCOptions(tt.cfg)

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
		env     map[string]string
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
			cfg:  config.Docker{Host: "tcp://10.0.0.1:2376", CertPath: "/certs"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				assert.True(t, got.TLSVerify)
				require.NotNil(t, got.TLSOptions)
				assert.False(t, got.TLSOptions.InsecureSkipVerify, "a certificate is verified unless asked otherwise")
				assert.Equal(t, "/certs/ca.pem", got.TLSOptions.CAFile)
				assert.Equal(t, "/certs/cert.pem", got.TLSOptions.CertFile)
				assert.Equal(t, "/certs/key.pem", got.TLSOptions.KeyFile)
			},
		},
		{
			name: "explicit files override the cert path per file",
			cfg:  config.Docker{CertPath: "/certs", CAFile: "/etc/ca.pem", KeyFile: "/etc/key.pem"},
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
			name: "skip_tls_verify alone turns tls on, with no cert material",
			cfg:  config.Docker{Host: "tcp://10.0.0.1:2376", SkipTLSVerify: true},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				assert.False(t, got.TLSVerify)
				require.NotNil(t, got.TLSOptions)
				assert.True(t, got.TLSOptions.InsecureSkipVerify)
				assert.Empty(t, got.TLSOptions.CAFile)
			},
		},
		{
			// A CA to verify against, but no client certificate — still a complete
			// config, because cert/key are a pair that is simply absent here.
			name: "a ca file alone verifies without a client certificate",
			cfg:  config.Docker{CAFile: "/etc/ca.pem"},
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
		{
			name: "the environment alone turns tls on, with the three pem paths",
			env:  map[string]string{client.EnvOverrideHost: "tcp://h:1", client.EnvTLSVerify: "1", client.EnvOverrideCertPath: "/env-certs"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.True(t, got.TLS)
				assert.True(t, got.TLSVerify)
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/env-certs/ca.pem", got.TLSOptions.CAFile)
				assert.Equal(t, "/env-certs/cert.pem", got.TLSOptions.CertFile)
				assert.Equal(t, "/env-certs/key.pem", got.TLSOptions.KeyFile)
			},
		},
		{
			name: "a cert path in the environment alone keeps tls off",
			env:  map[string]string{client.EnvOverrideHost: "tcp://h:1", client.EnvOverrideCertPath: "/env-certs"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.False(t, got.TLS)
				assert.Nil(t, got.TLSOptions)
			},
		},
		{
			name:    "the environment's verify with no cert path is refused",
			env:     map[string]string{client.EnvOverrideHost: "tcp://h:1", client.EnvTLSVerify: "1"},
			wantErr: "DOCKER_TLS_VERIFY is set but DOCKER_CERT_PATH is not",
		},
		{
			name: "a stale verify with no tcp host changes nothing",
			env:  map[string]string{client.EnvTLSVerify: "1"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.False(t, got.TLS)
				assert.Nil(t, got.TLSOptions)
			},
		},
		{
			name: "half a client pair in the config stays refused beside the environment",
			cfg:  config.Docker{CertFile: "/etc/cert.pem"},
			env: map[string]string{
				client.EnvOverrideHost: "tcp://h:1", client.EnvTLSVerify: "1", client.EnvOverrideCertPath: "/env-certs",
			},
			wantErr: "docker.key_file is missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(client.EnvOverrideHost, "")
			t.Setenv(client.EnvTLSVerify, "")
			t.Setenv(client.EnvOverrideCertPath, "")
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

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

// DOCKER_TLS_VERIFY brings DOCKER_CERT_PATH in beside a tcp DOCKER_HOST the config does not
// override; any file the config names keeps it out, and no certificate at all is refused.
func TestWithDockerTLSEnv(t *testing.T) {
	const tcpHost = "tcp://h:1"
	tests := []struct {
		name    string
		cfg     config.Docker
		host    string
		verify  string
		path    string
		want    config.Docker
		wantErr string
	}{
		{name: "an empty environment changes nothing", cfg: config.Docker{CAFile: "/cfg/ca.pem"}, host: tcpHost, want: config.Docker{CAFile: "/cfg/ca.pem"}},
		{name: "both variables", host: tcpHost, verify: "1", path: "/env", want: config.Docker{CertPath: "/env"}},
		{name: "any value of DOCKER_TLS_VERIFY counts", host: tcpHost, verify: "0", path: "/env", want: config.Docker{CertPath: "/env"}},
		{name: "a cert path with no DOCKER_TLS_VERIFY is not taken", host: tcpHost, path: "/env"},
		{name: "DOCKER_TLS_VERIFY with no certificate is refused", host: tcpHost, verify: "1", wantErr: "DOCKER_TLS_VERIFY is set but DOCKER_CERT_PATH is not"},
		{name: "no certificate is fine under skip_tls_verify", cfg: config.Docker{SkipTLSVerify: true}, host: tcpHost, verify: "1", want: config.Docker{SkipTLSVerify: true}},
		{
			name:   "skip_tls_verify keeps the cert path for its client certificate",
			cfg:    config.Docker{SkipTLSVerify: true},
			host:   tcpHost,
			verify: "1",
			path:   "/env",
			want:   config.Docker{SkipTLSVerify: true, CertPath: "/env"},
		},
		{name: "no DOCKER_HOST: not taken", verify: "1", path: "/env"},
		{name: "a socket DOCKER_HOST: not taken", host: "unix:///d.sock", verify: "1", path: "/env"},
		{name: "an ssh DOCKER_HOST: not taken", host: "ssh://u@h", verify: "1", path: "/env"},
		{name: "a DOCKER_HOST docker/cli cannot parse: not taken", host: "http://h:1", verify: "1", path: "/env"},
		{name: "a DOCKER_HOST with no scheme is tcp", host: "h:1", verify: "1", path: "/env", want: config.Docker{CertPath: "/env"}},
		{name: "a DOCKER_HOST of a port alone is tcp", host: ":2376", verify: "1", path: "/env", want: config.Docker{CertPath: "/env"}},
		{name: "a DOCKER_HOST with spaces around it is tcp", host: " tcp://h:1 ", verify: "1", path: "/env", want: config.Docker{CertPath: "/env"}},
		{name: "a DOCKER_HOST with no scheme and no certificate is refused", host: "h:1", verify: "1", wantErr: "DOCKER_TLS_VERIFY is set but DOCKER_CERT_PATH is not"},
		{
			name:   "the config names a host: not taken",
			cfg:    config.Docker{Host: "tcp://plain:2375"},
			host:   tcpHost,
			verify: "1",
			path:   "/env",
			want:   config.Docker{Host: "tcp://plain:2375"},
		},
		{
			name:   "the config names a context: not taken",
			cfg:    config.Docker{Context: "remote"},
			host:   tcpHost,
			verify: "1",
			path:   "/env",
			want:   config.Docker{Context: "remote"},
		},
		{name: "the config's cert path wins", cfg: config.Docker{CertPath: "/cfg"}, host: tcpHost, verify: "1", path: "/env", want: config.Docker{CertPath: "/cfg"}},
		{name: "a ca file in the config keeps it out", cfg: config.Docker{CAFile: "/cfg/ca.pem"}, host: tcpHost, verify: "1", path: "/env", want: config.Docker{CAFile: "/cfg/ca.pem"}},
		{name: "a client cert in the config keeps it out", cfg: config.Docker{CertFile: "/cfg/c.pem"}, host: tcpHost, verify: "1", path: "/env", want: config.Docker{CertFile: "/cfg/c.pem"}},
		{name: "a client key in the config keeps it out", cfg: config.Docker{KeyFile: "/cfg/k.pem"}, host: tcpHost, verify: "1", path: "/env", want: config.Docker{KeyFile: "/cfg/k.pem"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(client.EnvOverrideHost, tt.host)
			t.Setenv(client.EnvTLSVerify, tt.verify)
			t.Setenv(client.EnvOverrideCertPath, tt.path)

			// Act
			got, err := withDockerTLSEnv(tt.cfg)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// newDockerTLSOptions honours docker's two-part TLS model: a cert_file/key_file pair
// (mutual TLS) that must be supplied together, and ca_file governing server
// verification independently, which only skip_tls_verify turns off. The useTLS return is what keeps an all-empty
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
		wantSkip    bool
	}{
		{
			name:       "an empty config leaves tls off",
			cfg:        config.Docker{},
			wantUseTLS: false,
		},
		{
			name:       "skip_tls_verify alone turns tls on with no cert material",
			cfg:        config.Docker{SkipTLSVerify: true},
			wantUseTLS: true,
			wantSkip:   true,
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
			assert.Equal(t, tt.wantSkip, opts.InsecureSkipVerify)
			assert.Equal(t, tt.wantCAFile, opts.CAFile)
			assert.Equal(t, tt.wantCert, opts.CertFile)
			assert.Equal(t, tt.wantKeyFile, opts.KeyFile)
		})
	}
}

// Compose's progress is what turns a two-minute health-check wait from a hang
// into something a user can watch, so every channel must reach the library — and
// none of them may reach stdout, which carries arcave's JSON.
func TestNewComposeProgress(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		wantProcessor api.EventProcessor // nil means "the event logger"
		wantStream    io.Writer
	}{
		{
			name:       "log folds compose's events into arcave's own log",
			mode:       config.ProgressLog,
			wantStream: io.Discard,
		},
		{
			// Plain off a terminal, as here: compose's tree would spray ANSI escapes into a log file.
			name:          "tty hands stderr back to compose's own renderer",
			mode:          config.ProgressTTY,
			wantProcessor: display.Plain(io.Discard),
			wantStream:    os.Stderr,
		},
		{
			name:          "quiet discards the stream as well as the UI",
			mode:          config.ProgressQuiet,
			wantProcessor: display.Quiet(),
			wantStream:    io.Discard,
		},
		{
			name:       "an unset channel is the log",
			mode:       "",
			wantStream: io.Discard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newComposeProgress(tt.mode, zerolog.Nop())

			// Assert
			assert.Equal(t, tt.wantStream, got.stream)
			if tt.wantProcessor == nil {
				require.NotNil(t, got.events)
				assert.Same(t, got.events, got.processor, "the logger compose reports to is the one a step reads")
				return
			}
			assert.IsType(t, tt.wantProcessor, got.processor)
			assert.Nil(t, got.events)
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
	daemon, err := newDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, daemon.api.Close()) })
	assert.NotNil(t, daemon.cli)
	assert.NotNil(t, daemon.api)
	assert.Equal(t, "unix:///nowhere/docker.sock", daemon.transport.DaemonHost())
	assert.NotNil(t, daemon.transport.HTTPClient())
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
	daemon, err := newDockerClient(config.Docker{
		Host:       "unix:///nowhere/docker.sock",
		APIVersion: "1.45",
	}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, daemon.api.Close()) })
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
	daemon, err := newDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, daemon.api.Close()) })
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
	_, err := newDockerClient(config.Docker{Context: "nosuchcontext"}, io.Discard)

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
	_, err := newDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

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
	daemon, err := newDockerClient(config.Docker{Host: "tcp://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { _ = daemon.api.Close() })
	engine := docker.NewEngine(daemon.api)
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
	daemon, err := newDockerClient(config.Docker{Host: "tcp://127.0.0.1:1"}, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { _ = daemon.api.Close() })
	engine := docker.NewEngine(daemon.api)
	type resolution struct {
		collector model.GarbageCollectorKind
		daemon    bool
	}
	want := map[model.GarbageDataKind]resolution{
		model.GarbageDataKindRestoredLayers:      {collector: model.GarbageCollectorKindDockerLayerTags, daemon: true},
		model.GarbageDataKindSavepointData:       {collector: model.GarbageCollectorKindSavepointDirectory},
		model.GarbageDataKindSafetySnapshots:     {collector: model.GarbageCollectorKindSafetySnapshotRetention},
		model.GarbageDataKindUpstreamImages:      {collector: model.GarbageCollectorKindOCIImages},
		model.GarbageDataKindImageLayers:         {collector: model.GarbageCollectorKindOCIBlobs},
		model.GarbageDataKindDeduplicatedVolumes: {collector: model.GarbageCollectorKindResticRepositories},
	}

	// Act
	got := map[model.GarbageDataKind][]resolution{}
	for _, collector := range newDaemonCollectors(config.Config{}, engine) {
		got[collector.DataKind()] = append(got[collector.DataKind()], resolution{collector: collector.Kind(), daemon: true})
	}
	for _, collector := range newComposeProjectCollectors(config.Config{}, engine) {
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

// gc.safety_snapshots.retain_recent reaches the collector that judges by it.
func TestNewComposeProjectCollectors_Retention(t *testing.T) {
	var (
		labels     = map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
		savepoints = []model.Savepoint{
			{ID: "019e843e-1650-7000-8000-00000000000a", Labels: labels},
			{ID: "019e843e-1650-7000-8000-00000000000b", Labels: labels},
			{ID: "019e843e-1650-7000-8000-00000000000c", Labels: labels},
		}
	)
	tests := []struct {
		name         string
		retainRecent int
		wantFound    int
	}{
		{name: "0 deletes every one", retainRecent: 0, wantFound: 3},
		{name: "2 keeps two", retainRecent: 2, wantFound: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			cfg := config.Config{GC: config.GC{SafetySnapshots: config.GCSafetySnapshots{RetainRecent: &tt.retainRecent}}}
			collectors := newComposeProjectCollectors(cfg, nil)
			index := slices.IndexFunc(collectors, func(collector gc.ComposeProjectCollector) bool {
				return collector.DataKind() == model.GarbageDataKindSafetySnapshots
			})
			require.GreaterOrEqual(t, index, 0)

			// Act
			stat, err := collectors[index].Find(context.Background(), gc.ComposeProjectScope{Savepoints: savepoints})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, stat.Summary().Found)
		})
	}
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
		model.GarbageActionDeleteTree, model.GarbageActionDeleteSavepoint, model.GarbageActionDeleteImage,
		model.GarbageActionDeleteObject, model.GarbageActionForget,
	}

	// Act
	var got []model.GarbageAction
	for _, eraser := range newComposeProjectErasers(config.Config{}, nil) {
		got = append(got, eraser.Action())
	}

	// Assert
	assert.Equal(t, want, got)
}

// The delete-savepoint eraser the DI root builds takes the record and the tree through the real
// repository, and counts what the finding said, for a tree holding only its record too.
func TestNewComposeProjectErasers_DeleteSavepoint(t *testing.T) {
	const (
		whole      = model.SavepointID("019e843e-1650-7000-8000-000000000000")
		recordOnly = model.SavepointID("019e844b-7818-7000-8000-000000000000")
		kept       = model.SavepointID("019e8450-0000-7000-8000-000000000000")
	)
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	repo := sprepo.New(store)
	for _, id := range []model.SavepointID{whole, recordOnly, kept} {
		require.NoError(t, repo.SaveSavepoint(ctx, model.Savepoint{ID: id}))
	}
	data := whole.String() + "/volume-named-a/data.tar"
	require.NoError(t, store.PutObject(ctx, data, strings.NewReader("archive")))
	var savepointEraser gc.ComposeProjectEraser
	for _, eraser := range newComposeProjectErasers(config.Config{}, nil) {
		if eraser.Action() == model.GarbageActionDeleteSavepoint {
			savepointEraser = eraser
		}
	}
	require.NotNil(t, savepointEraser)
	wholeBytes, recordBytes := int64(7), int64(3)
	ops := []model.GarbageOp{
		{Action: model.GarbageActionDeleteSavepoint, Target: whole.String(), Bytes: &wholeBytes},
		{Action: model.GarbageActionDeleteSavepoint, Target: recordOnly.String(), Bytes: &recordBytes},
	}

	// Act
	got := savepointEraser.Erase(ctx, store, ops)

	// Assert
	assert.Equal(t, model.GarbageDeletion{Deleted: []model.GarbageDeleted{
		{Target: whole.String(), Bytes: &wholeBytes},
		{Target: recordOnly.String(), Bytes: &recordBytes},
	}}, got)
	exists, err := store.Exists(ctx, data)
	require.NoError(t, err)
	assert.False(t, exists, "the tree goes with the record")
	left, err := repo.ListSavepoints(ctx)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, kept, left[0].ID)
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
	sink.Event(compose.Event{Kind: "Volume", Name: "demo_kept", Text: "not recreated", State: compose.EventWarning})
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
		{progress.StatusWarned, "not recreated", progress.Resource{Kind: "volume", ID: "demo_kept"}, false},
	}, got)
}

func TestNewComposeEventSink_MuteNodeKeepsTheLog(t *testing.T) {
	// Act
	sink := newComposeEventSink(context.Background())

	// Assert
	assert.Nil(t, sink)
}

// The engine decides the profile whole: Podman is reached past its compat API, over TLS as the
// endpoint is, held to no Docker version, with its own defaults; Docker through the API itself.
func TestNewRuntimeProfile(t *testing.T) {
	api, err := client.New(client.WithHost("tcp://10.0.0.5:2376"))
	require.NoError(t, err)
	transport := daemonTransport{
		client: &http.Client{},
		host:   api.DaemonHost(),
	}
	info := system.Info{Name: "host-1", LoggingDriver: "json-file", DefaultRuntime: "runc"}
	plain := dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2375"}}
	dockerEngine := docker.NewEngine(api)
	libpodPlain, err := libpod.NewClient(transport, libpod.Options{Version: "6.1.4"})
	require.NoError(t, err)
	libpodTLS, err := libpod.NewClient(transport, libpod.Options{
		Version: "6.1.4",
		UseTLS:  true,
	})
	require.NoError(t, err)
	podmanEngine := func(libpodClient *libpod.Client) *docker.Engine {
		return docker.NewEngine(podman.NewAPI(api, libpodClient))
	}
	tests := []struct {
		name     string
		runtime  model.ContainerRuntime
		endpoint dockercontext.Endpoint
		want     runtimeProfile
		wantErr  string
	}{
		{
			name:     "docker",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: plain,
			want: runtimeProfile{
				api:            api,
				engine:         dockerEngine,
				volumes:        dockerEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
		{
			name:    "docker behind TLS",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			want: runtimeProfile{
				api:            api,
				engine:         dockerEngine,
				volumes:        dockerEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
		{
			name:     "podman",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: plain,
			want: runtimeProfile{
				api:            podman.NewAPI(api, libpodPlain),
				engine:         podmanEngine(libpodPlain),
				volumes:        podman.NewEngine(podmanEngine(libpodPlain)),
				version:        "6.1.4",
				minDaemonMajor: 0,
				assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
			},
		},
		{
			name:     "podman reporting no version is refused",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman},
			endpoint: plain,
			wantErr:  "reach libpod: no Podman version",
		},
		{
			name:    "podman behind TLS reaches libpod over TLS",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			want: runtimeProfile{
				api:            podman.NewAPI(api, libpodTLS),
				engine:         podmanEngine(libpodTLS),
				volumes:        podman.NewEngine(podmanEngine(libpodTLS)),
				version:        "6.1.4",
				minDaemonMajor: 0,
				assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
			},
		},
		{
			name:    "podman behind an https:// host of a context is refused",
			runtime: model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{
				EndpointMeta: dockercontext.EndpointMeta{Host: "https://10.0.0.5:2376"},
				TLSData:      &clicontext.TLSData{},
			},
			wantErr: "name it tcp://",
		},
		{
			name:     "podman behind an http:// host of a context is refused",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "http://10.0.0.5:2375"}},
			wantErr:  "name it tcp://",
		},
		{
			name:     "docker behind an http:// host of a context is not refused",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimeDocker, Version: "29.8.2"},
			endpoint: dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "http://10.0.0.5:2375"}},
			want: runtimeProfile{
				api:            api,
				engine:         dockerEngine,
				volumes:        dockerEngine,
				version:        "29.8.2",
				minDaemonMajor: dedupcopy.MinDaemonMajor,
				assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
			},
		},
		{
			name:     "podman with TLS verification off reaches libpod over TLS too",
			runtime:  model.ContainerRuntime{Kind: model.ContainerRuntimePodman, Version: "6.1.4"},
			endpoint: dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376", SkipTLSVerify: true}},
			want: runtimeProfile{
				api:            podman.NewAPI(api, libpodTLS),
				engine:         podmanEngine(libpodTLS),
				volumes:        podman.NewEngine(podmanEngine(libpodTLS)),
				version:        "6.1.4",
				minDaemonMajor: 0,
				assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := newRuntimeProfile(tt.runtime, info, api, transport, tt.endpoint)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, runtimeProfile{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The endpoint the client resolved decides how libpod is reached: a daemon behind TLS over
// https, a plain one over http.
func TestReadRuntimeProfile(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = io.WriteString(w, `{"Version":"6.1.4","Components":[{"Name":"Podman Engine","Version":"6.1.4"}]}`)
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = io.WriteString(w, `{}`)
		}
	})
	tests := []struct {
		name    string
		newCase func(t *testing.T) config.Docker
		wantTLS bool
	}{
		{
			name: "a plain daemon",
			newCase: func(t *testing.T) config.Docker {
				t.Helper()
				server := httptest.NewServer(handler)
				t.Cleanup(server.Close)
				return config.Docker{Host: "tcp://" + strings.TrimPrefix(server.URL, "http://")}
			},
		},
		{
			name: "a daemon behind TLS",
			newCase: func(t *testing.T) config.Docker {
				t.Helper()
				server := httptest.NewTLSServer(handler)
				t.Cleanup(server.Close)
				caFile := filepath.Join(t.TempDir(), "ca.pem")
				caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
				require.NoError(t, os.WriteFile(caFile, caPEM, 0o600))
				return config.Docker{Host: "tcp://" + strings.TrimPrefix(server.URL, "https://"), CAFile: caFile}
			},
			wantTLS: true,
		},
		{
			name: "a daemon behind TLS under skip_tls_verify, with no certificate",
			newCase: func(t *testing.T) config.Docker {
				t.Helper()
				server := httptest.NewTLSServer(handler)
				t.Cleanup(server.Close)
				return config.Docker{Host: "tcp://" + strings.TrimPrefix(server.URL, "https://"), SkipTLSVerify: true}
			},
			wantTLS: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			opened := &closers{}
			defer opened.release(context.Background())
			daemon, err := connectDaemon(tt.newCase(t), io.Discard, opened)
			require.NoError(t, err)
			libpodClient, err := libpod.NewClient(daemon.transport, libpod.Options{
				Version: "6.1.4",
				UseTLS:  tt.wantTLS,
			})
			require.NoError(t, err)

			// Act
			got, err := readRuntimeProfile(context.Background(), daemon)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, podman.NewAPI(daemon.api, libpodClient), got.api)
		})
	}
}

// The tools commands reach the daemon first: one that does not answer fails the wiring.
func TestOpenComposeFileService_DaemonUnreachable(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	ctx := zerolog.Nop().WithContext(context.Background())
	cfg := config.Config{Docker: config.Docker{Host: "tcp://127.0.0.1:1"}}
	var opened closers
	t.Cleanup(func() { opened.release(ctx) })

	// Act
	svc, err := openComposeFileService(ctx, cfg, "demo", &opened)

	// Assert
	require.ErrorContains(t, err, "read the daemon version")
	assert.Nil(t, svc)
}

// newUnreachableDaemonConfig is a project over a directory store and a daemon that does not answer.
func newUnreachableDaemonConfig(t *testing.T, projectPath string) config.Config {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	cfg := config.Config{
		Project: config.Project{Name: "app"},
		Docker:  config.Docker{Host: "tcp://127.0.0.1:1"},
		Storage: config.Storage{
			Type:      config.StorageTypeDirectory,
			Directory: &config.DirectoryStorage{Path: t.TempDir()},
		},
	}
	cfg.ApplyDefaults(projectPath)
	return cfg
}

// savepoint list and remove reach no daemon and build no backend: against a daemon that does not
// answer and a compression no factory takes, a savepoint of any backend is listed and removed whole,
// in a session of its own.
func TestOpenSavepointStoreService(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	projectPath := t.TempDir()
	cfg := newUnreachableDaemonConfig(t, projectPath)
	cfg.Backends.FullCopy.Compression = "lz4"

	var opened closers
	t.Cleanup(func() { opened.release(ctx) })
	sessions, err := openSessionService(ctx, projectPath, cfg, &opened)
	require.NoError(t, err)
	_, err = sessions.CreateSession(ctx, model.CreateSessionRequest{Name: "exp"})
	require.NoError(t, err)

	scope, err := openComposeProjectScope(ctx, cfg, projectPath, "exp", "demo", &opened)
	require.NoError(t, err)
	savepoint := model.Savepoint{
		ID: "019e843e-1650-7000-8000-000000000000",
		Stats: []model.SnapshotStat{
			{Volume: model.Volume{ObjectID: "volume-named-a", Backend: model.SnapshotBackendFullCopy}},
			{Volume: model.Volume{ObjectID: "volume-named-b", Backend: model.SnapshotBackendCDC}},
			{Volume: model.Volume{ObjectID: "volume-named-c", Backend: "retired"}},
		},
	}
	require.NoError(t, sprepo.New(scope.store).SaveSavepoint(ctx, savepoint))
	archive := savepoint.ID.String() + "/volume-named-a/data.tar"
	require.NoError(t, scope.store.PutObject(ctx, archive, strings.NewReader("archive")))

	svc, err := openSavepointStoreService(ctx, projectPath, cfg, "exp", "demo", &opened)
	require.NoError(t, err)

	// Act
	listed, listErr := svc.ListSavepoints(ctx, model.ListRequest{Sort: model.SortAsc})
	removed, removeErr := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{savepoint.ID.String()}})
	left, leftErr := svc.ListSavepoints(ctx, model.ListRequest{Sort: model.SortAsc})

	// Assert
	require.NoError(t, listErr)
	require.NoError(t, removeErr)
	require.NoError(t, leftErr)
	require.Len(t, listed, 1)
	assert.Equal(t, savepoint.ID, listed[0].ID)
	require.Len(t, removed.Removed, 1)
	assert.Equal(t, savepoint.ID, removed.Removed[0].ID)
	assert.Empty(t, left)
	exists, err := scope.store.Exists(ctx, archive)
	require.NoError(t, err)
	assert.False(t, exists, "the tree goes with the record")
}

// A workflow that needs the daemon fails on the wiring's first request.
func TestOpenSavepointService_DaemonUnreachable(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	projectPath := t.TempDir()
	cfg := newUnreachableDaemonConfig(t, projectPath)
	var opened closers
	t.Cleanup(func() { opened.release(ctx) })

	// Act
	svc, err := openSavepointService(ctx, projectPath, cfg, "", "demo", &opened)

	// Assert
	require.ErrorContains(t, err, "read the daemon version")
	assert.Nil(t, svc)
}

// A gc reads no profile: a daemon that does not answer is its collectors' probe, not the wiring's failure.
func TestOpenGCService_DaemonUnreachable(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	projectPath := t.TempDir()
	cfg := newUnreachableDaemonConfig(t, projectPath)
	var opened closers
	t.Cleanup(func() { opened.release(ctx) })

	// Act
	svc, err := openGCService(ctx, projectPath, cfg, "", &opened)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

// The client and libpod's transport go on the stack: nothing else closes their connections.
func TestConnectDaemon(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	var opened closers
	t.Cleanup(func() { opened.release(context.Background()) })

	// Act
	daemon, err := connectDaemon(config.Docker{Host: "tcp://127.0.0.1:1"}, io.Discard, &opened)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, daemon.api)
	assert.Len(t, opened.releases, 2)
}

// The name is gated before anything is opened, though the tools commands open no store.
func TestOpenComposeFileService_RefusesABadName(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	ctx := zerolog.Nop().WithContext(context.Background())
	cfg := config.Config{Docker: config.Docker{Host: "tcp://127.0.0.1:1"}}
	var opened closers

	// Act
	svc, err := openComposeFileService(ctx, cfg, "../x", &opened)

	// Assert
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "read the daemon version")
	assert.Nil(t, svc)
	assert.Empty(t, opened.releases, "nothing is opened")
}

// A factory left out of the literal is no compile error: it would panic on the first tool call.
func TestNewMCPFactories(t *testing.T) {
	// Act
	factories := reflect.ValueOf(newMCPFactories())

	// Assert
	for i := range factories.NumField() {
		assert.False(t, factories.Field(i).IsNil(), factories.Type().Field(i).Name)
	}
}

// mockCloser is one thing a wiring opened.
type mockCloser struct{ mock.Mock }

func (m *mockCloser) Close() error { return m.Called().Error(0) }

// The stack unwinds newest first, and a closer that fails stops none of the older ones.
func TestClosers_release(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	store, engine, server := &mockCloser{}, &mockCloser{}, &mockCloser{}
	mock.InOrder(
		server.On("Close").Return(nil).Once(),
		engine.On("Close").Return(errors.New("boom")).Once(),
		store.On("Close").Return(nil).Once(),
	)
	var opened closers
	opened.add("store", store)
	opened.add("engine", engine)
	opened.addFunc(func(context.Context) { assert.NoError(t, server.Close()) })

	// Act
	opened.release(ctx)

	// Assert
	store.AssertExpectations(t)
	engine.AssertExpectations(t)
	server.AssertExpectations(t)
}

func TestOpenArcaveProjectStore(t *testing.T) {
	key := newTestStoreKey(t)
	encrypted := func(t *testing.T, projectPath string) {
		t.Helper()
		var opened closers
		defer opened.release(t.Context())
		created, err := openArcaveProjectStore(t.Context(),
			newEncryptionConfig(t, projectPath, config.Encryption{Enabled: true, Key: key}), projectPath, &opened)
		require.NoError(t, err)
		require.NoError(t, created.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("a secret")))
	}
	plain := func(t *testing.T, projectPath string) {
		t.Helper()
		var opened closers
		defer opened.release(t.Context())
		created, err := openArcaveProjectStore(t.Context(), newEncryptionConfig(t, projectPath, config.Encryption{}), projectPath, &opened)
		require.NoError(t, err)
		require.NoError(t, created.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("{}")))
	}
	tests := []struct {
		name       string
		stored     func(t *testing.T, projectPath string)
		encryption config.Encryption
		wantErr    error
		wantMsg    string
		verify     func(t *testing.T, projectPath string, store storages.Storager)
	}{
		{
			name:       "an encrypted store with the key it was created with",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true, Key: key},
			verify: func(t *testing.T, projectPath string, store storages.Storager) {
				assert.Equal(t, "a secret", readStoreObject(t, store, "sessions/x/session.json"))
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
			name:       "an encrypted store with no key",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true},
			wantErr:    model.ErrInvalidStoreKey,
			wantMsg:    "none set (check encryption.key",
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
			verify: func(t *testing.T, _ string, store storages.Storager) {
				assert.Equal(t, "{}", readStoreObject(t, store, "sessions/x/session.json"))
			},
		},
		{
			name:       "a plain store with the key exported and encryption off",
			stored:     plain,
			encryption: config.Encryption{Key: key},
			verify: func(t *testing.T, _ string, store storages.Storager) {
				assert.Equal(t, "{}", readStoreObject(t, store, "sessions/x/session.json"), "the key is ignored")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			projectPath := t.TempDir()
			tt.stored(t, projectPath)
			before := listTree(t, arcaveProjectPath(projectPath))

			var opened closers
			t.Cleanup(func() { opened.release(context.Background()) })

			// Act
			store, err := openArcaveProjectStore(t.Context(), newEncryptionConfig(t, projectPath, tt.encryption), projectPath, &opened)

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
			tt.verify(t, projectPath, store)
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

// The transport libpod shares is the one the endpoint names: TLS off a tcp host only, a helper's
// dialer for ssh, the default socket for no host.
func TestNewDaemonTransport(t *testing.T) {
	tests := []struct {
		name     string
		endpoint dockercontext.EndpointMeta
		tlsData  *clicontext.TLSData
		wantTLS  bool
		wantErr  error
	}{
		{
			name:     "a tcp host under skip_tls_verify carries TLS",
			endpoint: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376", SkipTLSVerify: true},
			wantTLS:  true,
		},
		{
			name:     "a tcp host with no TLS material is plain",
			endpoint: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2375"},
		},
		{
			name:     "a unix socket carries no TLS whatever the endpoint says",
			endpoint: dockercontext.EndpointMeta{Host: "unix:///var/run/docker.sock", SkipTLSVerify: true},
		},
		{
			name: "no host is the default socket",
		},
		{
			name:     "ssh dials through the connection helper",
			endpoint: dockercontext.EndpointMeta{Host: "ssh://user@10.0.0.5"},
		},
		{
			name:     "a CA that is no certificate is refused",
			endpoint: dockercontext.EndpointMeta{Host: "tcp://10.0.0.5:2376"},
			tlsData:  &clicontext.TLSData{CA: []byte("not a certificate")},
			wantErr:  errInvalidEndpointCA,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := newDaemonTransport(dockercontext.Endpoint{EndpointMeta: tt.endpoint, TLSData: tt.tlsData})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			transport, ok := got.Transport.(*http.Transport)
			require.True(t, ok)
			assert.NotNil(t, transport.DialContext)
			assert.Equal(t, tt.wantTLS, transport.TLSClientConfig != nil)
			if tt.wantTLS {
				assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
			}
		})
	}
}
