package rest

import (
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/labstack/echo/v4"
)

// defaultAddress binds the loopback on a port the kernel picks.
const defaultAddress = "127.0.0.1:0"

// defaultScheme is what a repository URL carries when the caller names no scheme.
const defaultScheme = "http"

// Serve binds address — the loopback on a kernel-picked port when it is empty —
// hands the listener to e and serves in the background, returning the port
// actually bound. Closing e releases it.
//
// The port is resolved here, where a failure is still returnable, because it is
// the one thing a repository URL takes from the bind: everything below this line
// is handed a value, so nothing holds the server to ask it later.
func Serve(e *echo.Echo, address string) (string, error) {
	if address == "" {
		address = defaultAddress
	}

	ln, err := net.Listen("tcp", address)
	if err != nil {
		return "", fmt.Errorf("listen on tcp %s: %w", address, err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		if closeErr := ln.Close(); closeErr != nil {
			e.Logger.Errorf("close the listener of a server that never started: %v", closeErr)
		}
		return "", fmt.Errorf("resolve the bound port of %s: %w", ln.Addr(), err)
	}

	// Echo serves the listener it is given and binds none of its own, which is
	// what makes the bound port known before the first request.
	e.Listener = ln
	go serve(e)

	return port, nil
}

// serve runs e until it is closed, reporting anything else.
func serve(e *echo.Echo) {
	if err := e.Start(""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		e.Logger.Errorf("restic server failed: %v", err)
	}
}

// RepositoryURLBase renders the restic repository URL minus its path: restic's
// "rest:" prefix, scheme (http when empty), and the host as the container
// resolves it (never the bound address). A caller appends "<prefix>/".
func RepositoryURLBase(scheme, containerHost, port string) string {
	if scheme == "" {
		scheme = defaultScheme
	}
	return "rest:" + scheme + "://" + net.JoinHostPort(containerHost, port)
}
