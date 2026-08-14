package rest

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bind is the loopback on a port the kernel picks, and the port Serve
// returns is the port it bound — the one value everything downstream is handed
// in place of the server itself.
func TestServe(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{name: "an empty address binds the loopback on the kernel's port"},
		{name: "an address naming port 0 does the same", address: "127.0.0.1:0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			e := NewEcho(context.Background(), oneRepository(stubRepository{}))

			// Act
			port, err := Serve(e, tt.address)

			// Assert
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, e.Close()) })
			host, bound, err := net.SplitHostPort(e.ListenerAddr().String())
			require.NoError(t, err)
			assert.Equal(t, "127.0.0.1", host)
			assert.Equal(t, bound, port, "the port returned is the port bound")
			assert.NotEqual(t, "0", port, "the bound port replaces the requested one")
			assert.Equal(t, http.StatusOK, statusOf(t, e, "/testrepo/config"))
		})
	}
}

// An occupied port fails the wiring, before the command has touched the project.
func TestServe_OccupiedAddress(t *testing.T) {
	// Arrange: hold the address Serve is then asked for.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { assert.NoError(t, held.Close()) }()
	e := NewEcho(context.Background(), oneRepository(stubRepository{}))

	// Act
	port, err := Serve(e, held.Addr().String())

	// Assert
	require.Error(t, err)
	assert.Empty(t, port)
	assert.Contains(t, err.Error(), "listen on tcp "+held.Addr().String())
}

// Closing the app releases the port: it stops answering, and closing twice is
// not an error, since the DI root's unwind may reach it either way.
func TestServe_CloseReleasesThePort(t *testing.T) {
	// Arrange
	e := NewEcho(context.Background(), oneRepository(stubRepository{}))
	_, err := Serve(e, "")
	require.NoError(t, err)
	target := "http://" + e.ListenerAddr().String() + "/testrepo/config"

	// Act
	require.NoError(t, e.Close())

	// Assert
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // no response arrives
	require.Error(t, err)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.NoError(t, e.Close())
}

// The URL base is the whole of what goes down to a backend, so its shape is the
// contract: the scheme restic is handed, the name the *container* resolves, and
// the port Serve bound. The host is the caller's, since a listener cannot know
// what a container calls it.
func TestRepositoryURLBase(t *testing.T) {
	tests := []struct {
		name          string
		scheme        string
		containerHost string
		port          string
		want          string
	}{
		{
			name:          "the name a container on docker desktop resolves",
			containerHost: "host.docker.internal",
			port:          "39421",
			want:          "rest:http://host.docker.internal:39421",
		},
		{
			name:          "a client on the host names the loopback",
			containerHost: "127.0.0.1",
			port:          "8000",
			want:          "rest:http://127.0.0.1:8000",
		},
		{
			name:          "an ipv6 host is bracketed",
			containerHost: "::1",
			port:          "8000",
			want:          "rest:http://[::1]:8000",
		},
		{
			name:          "an empty scheme defaults to http",
			scheme:        "",
			containerHost: "127.0.0.1",
			port:          "8000",
			want:          "rest:http://127.0.0.1:8000",
		},
		{
			name:          "an explicit scheme is honoured",
			scheme:        "https",
			containerHost: "127.0.0.1",
			port:          "8000",
			want:          "rest:https://127.0.0.1:8000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			base := RepositoryURLBase(tt.scheme, tt.containerHost, tt.port)

			// Assert
			assert.Equal(t, tt.want, base)
		})
	}
}

// statusOf performs one GET against the address e is serving on.
func statusOf(t *testing.T, e *echo.Echo, path string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+e.ListenerAddr().String()+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, resp.Body.Close()) })
	return resp.StatusCode
}
