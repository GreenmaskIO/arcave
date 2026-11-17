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

package compose

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/docker/cli/cli/flags"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
)

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
	assert.Equal(t, "unix:///nowhere/docker.sock", daemon.api.DaemonHost())
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

// The client goes on the stack: nothing else closes its connections.
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
	assert.Len(t, opened.releases, 1)
}
