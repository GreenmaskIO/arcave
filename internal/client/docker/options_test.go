package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// env is what carries the options to the CLI half of this package. An unset
// option must not export an empty variable: that would clobber the inherited
// DOCKER_HOST rather than defer to it.
func TestOptions_env(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		wantVars   []string
		wantAbsent []string
	}{
		{
			name:       "the zero options export nothing",
			opts:       Options{},
			wantAbsent: []string{"DOCKER_HOST=", "DOCKER_CONTEXT=", "DOCKER_CERT_PATH=", "DOCKER_TLS_VERIFY=1"},
		},
		{
			name:     "a host",
			opts:     Options{Host: "tcp://10.0.0.1:2376"},
			wantVars: []string{"DOCKER_HOST=tcp://10.0.0.1:2376"},
		},
		{
			name: "tls settings",
			opts: Options{Host: "tcp://10.0.0.1:2376", CertPath: "/certs", TLSVerify: true},
			wantVars: []string{
				"DOCKER_HOST=tcp://10.0.0.1:2376",
				"DOCKER_CERT_PATH=/certs",
				"DOCKER_TLS_VERIFY=1",
			},
		},
		{
			name:       "tls verify off exports no flag",
			opts:       Options{TLSVerify: false},
			wantAbsent: []string{"DOCKER_TLS_VERIFY=1"},
		},
		{
			name:     "a context",
			opts:     Options{Host: "unix:///d.sock", Context: "remote"},
			wantVars: []string{"DOCKER_CONTEXT=remote"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv("DOCKER_HOST", "unix:///inherited.sock")

			// Act
			env := tt.opts.env()

			// Assert
			for _, want := range tt.wantVars {
				assert.Contains(t, env, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, env, absent)
			}
			assert.Contains(t, env, "DOCKER_HOST=unix:///inherited.sock",
				"the process environment must survive, so the user's own docker settings do")
		})
	}
}

// A docker context names a daemon the SDK client cannot resolve. Accepting it
// alone would leave the CLI half talking to that daemon and the SDK half talking
// to DOCKER_HOST's — so it is refused unless a host pins both.
func TestNewEngine_RejectsContextWithoutHost(t *testing.T) {
	// Act
	_, err := NewEngine(Options{Context: "remote"})

	// Assert
	require.ErrorContains(t, err, "needs docker.host set too")
}

func TestNewEngine_AcceptsAHost(t *testing.T) {
	// Act
	eng, err := NewEngine(Options{Host: "unix:///var/run/docker.sock"})

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, "unix:///var/run/docker.sock", eng.cli.DaemonHost())
}
