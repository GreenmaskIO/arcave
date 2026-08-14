package stack

import (
	"io"
	"os"
	"testing"

	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/backend/restic/cdc"
	arcavecompose "github.com/greenmaskio/arcave/internal/provider/docker/stack/internal/client/compose"
)

func TestCDCOptions(t *testing.T) {
	tests := []struct {
		name string
		cfg  CDCBackend
		want cdc.Options
	}{
		{
			name: "a defaulted section resolves to the stock image, incremental",
			cfg:  defaultedCDC(),
			want: cdc.Options{
				Image:       DefaultResticImage,
				Overwrite:   OverwriteAlways,
				Incremental: true,
			},
		},
		{
			name: "wipe is not incremental, whatever overwrite says",
			cfg: CDCBackend{
				RestoreMode: RestoreModeWipe,
				Overwrite:   OverwriteAlways,
				Image:       DefaultResticImage,
			},
			want: cdc.Options{
				Image:     DefaultResticImage,
				Overwrite: OverwriteAlways,
			},
		},
		{
			name: "incremental carries its overwrite policy through",
			cfg: CDCBackend{
				RestoreMode: RestoreModeIncremental,
				Overwrite:   OverwriteIfChanged,
				Image:       "restic/restic:0.18.0",
			},
			want: cdc.Options{
				Image:       "restic/restic:0.18.0",
				Incremental: true,
				Overwrite:   OverwriteIfChanged,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := cdcOptions(tt.cfg)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// An unset option must not pass through: an empty host would override DOCKER_HOST.
func TestGetDockerOptions(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Docker
		wantErr string
		verify  func(t *testing.T, got *flags.ClientOptions)
	}{
		{
			name: "the zero config passes nothing through",
			cfg:  Docker{},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.Empty(t, got.Hosts)
				assert.Empty(t, got.Context)
				assert.False(t, got.TLS)
				assert.Nil(t, got.TLSOptions)
			},
		},
		{
			name: "a host",
			cfg:  Docker{Host: "tcp://10.0.0.1:2376"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.Equal(t, []string{"tcp://10.0.0.1:2376"}, got.Hosts)
			},
		},
		{
			name: "tls settings expand to the three pem paths",
			cfg:  Docker{Host: "tcp://10.0.0.1:2376", CertPath: "/certs", TLSVerify: true},
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
			cfg:  Docker{CertPath: "/certs", CAFile: "/etc/ca.pem", KeyFile: "/etc/key.pem", TLSVerify: true},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/etc/ca.pem", got.TLSOptions.CAFile, "explicit CAFile wins")
				assert.Equal(t, "/certs/cert.pem", got.TLSOptions.CertFile, "unset file falls back to the cert path")
				assert.Equal(t, "/etc/key.pem", got.TLSOptions.KeyFile, "explicit KeyFile wins")
			},
		},
		{
			name: "explicit files alone enable tls without a cert path",
			cfg:  Docker{CAFile: "/etc/ca.pem", CertFile: "/etc/cert.pem", KeyFile: "/etc/key.pem"},
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
			cfg:  Docker{TLSVerify: false},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.False(t, got.TLS)
				assert.Nil(t, got.TLSOptions)
			},
		},
		{
			name: "a cert path alone enables tls with verify off",
			cfg:  Docker{CertPath: "/certs"},
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
			name: "a context alone is enough",
			cfg:  Docker{Context: "remote"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				assert.Equal(t, "remote", got.Context)
				assert.Empty(t, got.Hosts)
			},
		},
		{
			// The public-CA case: system roots verify the daemon, no client certificate.
			name: "tls verify alone enables tls with no cert material",
			cfg:  Docker{TLSVerify: true},
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
			name: "a ca file alone verifies without a client certificate",
			cfg:  Docker{CAFile: "/etc/ca.pem", TLSVerify: true},
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
			cfg:     Docker{CertFile: "/etc/cert.pem"},
			wantErr: "connection.key_file is missing",
		},
		{
			name:    "a client key without its cert is refused",
			cfg:     Docker{KeyFile: "/etc/key.pem"},
			wantErr: "connection.cert_file is missing",
		},
		{
			name: "a cert path completes a half-specified pair",
			cfg:  Docker{CertPath: "/certs", CertFile: "/etc/cert.pem"},
			verify: func(t *testing.T, got *flags.ClientOptions) {
				require.NotNil(t, got.TLSOptions)
				assert.Equal(t, "/etc/cert.pem", got.TLSOptions.CertFile, "explicit CertFile wins")
				assert.Equal(t, "/certs/key.pem", got.TLSOptions.KeyFile, "the missing key falls back to the cert path")
			},
		},
		{
			name:    "a host and a context together name two daemons",
			cfg:     Docker{Host: "unix:///d.sock", Context: "remote"},
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

// dockerTLSOptions honours docker's two-part TLS model: a cert_file/key_file
// pair supplied together, and ca_file/tls_verify governing verification.
func TestDockerTLSOptions(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Docker
		wantErr     string
		wantUseTLS  bool
		wantCAFile  string
		wantCert    string
		wantKeyFile string
	}{
		{
			name:       "an empty config leaves tls off",
			cfg:        Docker{},
			wantUseTLS: false,
		},
		{
			name:       "tls verify alone turns tls on with no cert material",
			cfg:        Docker{TLSVerify: true},
			wantUseTLS: true,
		},
		{
			name:       "a ca file alone verifies without a client certificate",
			cfg:        Docker{CAFile: "/etc/ca.pem"},
			wantUseTLS: true,
			wantCAFile: "/etc/ca.pem",
		},
		{
			name:        "a full pair enables tls",
			cfg:         Docker{CertFile: "/etc/cert.pem", KeyFile: "/etc/key.pem"},
			wantUseTLS:  true,
			wantCert:    "/etc/cert.pem",
			wantKeyFile: "/etc/key.pem",
		},
		{
			name:        "a cert path expands to the three pem paths",
			cfg:         Docker{CertPath: "/certs"},
			wantUseTLS:  true,
			wantCAFile:  "/certs/ca.pem",
			wantCert:    "/certs/cert.pem",
			wantKeyFile: "/certs/key.pem",
		},
		{
			name:        "a cert path completes a half-specified pair",
			cfg:         Docker{CertPath: "/certs", KeyFile: "/etc/key.pem"},
			wantUseTLS:  true,
			wantCAFile:  "/certs/ca.pem",
			wantCert:    "/certs/cert.pem",
			wantKeyFile: "/etc/key.pem",
		},
		{
			name:    "a client cert without its key is refused",
			cfg:     Docker{CertFile: "/etc/cert.pem"},
			wantErr: "connection.key_file is missing",
		},
		{
			name:    "a client key without its cert is refused",
			cfg:     Docker{KeyFile: "/etc/key.pem"},
			wantErr: "connection.cert_file is missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			opts, useTLS, err := dockerTLSOptions(tt.cfg)

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

// Every progress channel must reach the library, and none of them stdout.
func TestComposeProgressWriter(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		wantMode string
		wantOut  io.Writer // nil means "the adapter", which has no comparable value
	}{
		{
			name: "log folds compose's events into arcave's own log",
			mode: ProgressLog, wantMode: progress.ModeJSON,
		},
		{
			// ModeAuto, not ModeTTY: compose falls back to plain lines in a pipe.
			name: "tty hands the terminal back to compose's own renderer",
			mode: ProgressTTY, wantMode: progress.ModeAuto, wantOut: os.Stderr,
		},
		{
			name: "quiet discards the stream as well as the UI",
			mode: ProgressQuiet, wantMode: progress.ModeQuiet, wantOut: io.Discard,
		},
		{
			name: "an unset channel is the log",
			mode: "", wantMode: progress.ModeJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: progress.Mode is the library's global, so it is restored.
			original := progress.Mode
			t.Cleanup(func() { progress.Mode = original })

			// Act
			out := composeProgressWriter(tt.mode, zerolog.Nop())

			// Assert
			assert.Equal(t, tt.wantMode, progress.Mode)
			if tt.wantOut == nil {
				assert.IsType(t, &arcavecompose.ProgressLogger{}, out)
				return
			}
			// Streams arcave does not own are wrapped: closing os.Stderr would take
			// the process's error stream down.
			assert.Equal(t, nopWriteCloser{tt.wantOut}, out)
			assert.NoError(t, out.Close())
		})
	}
}

// A host resolves to an endpoint whether or not anything is listening on it:
// a control plane has to start with docker stopped.
func TestInitDockerClient_AcceptsAnUnreachableHost(t *testing.T) {
	// Arrange: an empty config dir, so the developer's own docker config cannot
	// steer the resolution.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	// Act
	dockerCli, apiClient, err := initDockerClient(Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.NotNil(t, dockerCli)
	assert.NotNil(t, apiClient)
}

// api_version is carried through the environment rather than ClientOptions.
func TestInitDockerClient_PinsAPIVersion(t *testing.T) {
	// Arrange: an empty config dir, and a known-empty env var t restores afterwards.
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv(client.EnvOverrideAPIVersion, "")

	// Act
	_, apiClient, err := initDockerClient(Docker{
		Host:       "unix:///nowhere/docker.sock",
		APIVersion: "1.45",
	}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.Equal(t, "1.45", os.Getenv(client.EnvOverrideAPIVersion),
		"cfg.APIVersion must reach docker/cli through DOCKER_API_VERSION")
}

// An empty api_version means "read the environment", not "clear it".
func TestInitDockerClient_UnsetAPIVersionLeavesTheEnvUntouched(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv(client.EnvOverrideAPIVersion, "1.40")

	// Act
	_, apiClient, err := initDockerClient(Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, apiClient.Close()) })
	assert.Equal(t, "1.40", os.Getenv(client.EnvOverrideAPIVersion),
		"an unset APIVersion must not disturb a version already in the environment")
}

// docker/cli answers an unknown context with os.Exit(1), so the endpoint is
// resolved while a failure is still returnable.
func TestInitDockerClient_RejectsAnUnknownContext(t *testing.T) {
	// Arrange: an empty config dir, so no context store can define the name.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	// Act
	_, _, err := initDockerClient(Docker{Context: "nosuchcontext"}, io.Discard)

	// Assert
	require.ErrorContains(t, err, "nosuchcontext")
}

// The other half of the os.Exit(1) surface: the endpoint resolves and building
// the client from it fails.
func TestInitDockerClient_RejectsAnUnbuildableClient(t *testing.T) {
	// Arrange
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_CUSTOM_HEADERS", "not-a-key-value-pair")

	// Act
	_, _, err := initDockerClient(Docker{Host: "unix:///nowhere/docker.sock"}, io.Discard)

	// Assert
	require.ErrorContains(t, err, "DOCKER_CUSTOM_HEADERS")
}

// Everything that can fail on configuration alone must fail in New — the decode
// of the blob the control plane carried included.
func TestNew(t *testing.T) {
	validSettings := func() model.RawSettings {
		return model.RawSettings{
			"connection": map[string]any{"host": "unix:///nowhere/docker.sock"},
			"storage": map[string]any{
				"type":      storageTypeDirectory,
				"directory": map[string]any{"path": t.TempDir()},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(s model.RawSettings)
		wantErr string
	}{
		{name: "a resolved provider is built"},
		{
			name:    "a storage arm the tag does not name is refused here",
			mutate:  func(s model.RawSettings) { delete(s["storage"].(map[string]any), "directory") },
			wantErr: "no storage.directory section is present",
		},
		{
			name:    "an unknown docker context is refused here",
			mutate:  func(s model.RawSettings) { s["connection"] = map[string]any{"context": "nosuchcontext"} },
			wantErr: "nosuchcontext",
		},
		{
			name:    "a progress channel compose does not know is refused here",
			mutate:  func(s model.RawSettings) { s["compose"] = map[string]any{"progress": "fireworks"} },
			wantErr: "unsupported compose.progress",
		},
		{
			// The control plane cannot judge a key it does not understand, so the
			// only place a typo under settings is caught is here.
			name:    "a key no setting claims is refused here",
			mutate:  func(s model.RawSettings) { s["sandbox"] = map[string]any{"image": "x"} },
			wantErr: "sandbox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			original := progress.Mode
			t.Cleanup(func() { progress.Mode = original })
			settings := validSettings()
			if tt.mutate != nil {
				tt.mutate(settings)
			}

			// Act
			p, err := New(t.Context(), settings)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, p)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, p.Close(t.Context())) })
			assert.Equal(t, model.ProviderCompose, p.Kind())
		})
	}
}

// The three verbs are stubs and say so rather than answering with a zero value.
func TestProvider_VerbsAreNotImplemented(t *testing.T) {
	// Arrange
	p := &Provider{}

	// Act & Assert
	_, err := p.Capture(t.Context(), provider.CaptureRequest{})
	require.ErrorContains(t, err, "not implemented")
	require.ErrorContains(t, p.Restore(t.Context(), provider.RestoreRequest{}), "not implemented")
	require.ErrorContains(t, p.Discard(t.Context(), provider.DiscardRequest{}), "not implemented")
}
