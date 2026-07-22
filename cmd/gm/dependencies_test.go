package main

import (
	"testing"

	"github.com/docker/cli/cli/flags"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/config"
)

// getDockerOptions is what carries the config into docker/cli. An unset option
// must not be passed through: an empty host would override the environment's
// DOCKER_HOST rather than defer to it.
func TestGetDockerOptions(t *testing.T) {
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
			name:    "a host and a context together name two daemons",
			cfg:     config.Docker{Host: "unix:///d.sock", Context: "remote"},
			wantErr: "mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := getDockerOptions(tt.cfg)

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

// A host resolves to an endpoint whether or not anything is listening on it:
// gm savepoint list has to work with docker stopped.
func TestInitDockerClient_AcceptsAnUnreachableHost(t *testing.T) {
	// Arrange: an empty config dir, so the developer's own docker config cannot
	// steer the resolution.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	// Act
	dockerCli, apiClient, err := initDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"})

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.NotNil(t, dockerCli)
	assert.NotNil(t, apiClient)
}

// A context that does not exist must surface as an error. docker/cli's own
// answer is to print to the (discarded) error stream and os.Exit(1), which would
// kill GM with no output at all — so the endpoint is resolved up front, where a
// failure is still returnable.
func TestInitDockerClient_RejectsAnUnknownContext(t *testing.T) {
	// Arrange: an empty config dir, so no context store can define the name.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	// Act
	_, _, err := initDockerClient(config.Docker{Context: "nosuchcontext"})

	// Assert
	require.ErrorContains(t, err, "nosuchcontext")
}

// The other half of the os.Exit(1) surface, and the one a check on the resolved
// endpoint's Host misses: here the endpoint resolves fine and it is building a
// client from it that fails, so docker/cli holds the failure behind a perfectly
// good Host and only answers on the next Client() call — by exiting. Building
// the client up front is what turns it into an error.
func TestInitDockerClient_RejectsAnUnbuildableClient(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_CUSTOM_HEADERS", "not-a-key-value-pair")

	// Act
	_, _, err := initDockerClient(config.Docker{Host: "unix:///nowhere/docker.sock"})

	// Assert
	require.ErrorContains(t, err, "DOCKER_CUSTOM_HEADERS")
}
