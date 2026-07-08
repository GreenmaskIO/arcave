package docker

import (
	"context"
	"strconv"
)

// DefaultUpWaitTimeout is the bound (seconds) Up applies to its health-check
// wait when the caller names none. Without a bound, a service that never becomes
// healthy (a crash-looping container with restart: always) would block forever.
//
// It is the default of *this* implementation, not a property of the domain: a
// runner that did not shell out to `docker compose up --wait` would have no use
// for it. Callers that expose it — the command layer, for a flag default — take
// it from here.
const DefaultUpWaitTimeout = 120

// ComposeRunner drives the *running* stack's lifecycle: it stops and starts the
// project's services, mirroring how the user would invoke `docker compose` by
// hand. Compose is where the Inspector's declared state becomes containers, and
// this is the type that moves it between the two — GM stops the services before
// touching a mount and starts them after, and it is ComposeRunner that does both.
//
// It shells out to the docker CLI rather than the Docker SDK because the Docker
// API has no Compose equivalent, and the CLI inherits the user's context,
// credentials and build settings. "Runner", not "client": the transport is an
// implementation detail that may become the SDK one day; the contract is
// "run the project's lifecycle".
//
// It is stateless: the compose files to act on are passed per call, so a single
// instance is shared across invocations rather than bound to a fixed file set.
type ComposeRunner struct {
	// run executes a docker CLI invocation; it is built by newRunner and is a
	// field only so tests can capture the assembled arguments without a daemon.
	run func(ctx context.Context, args ...string) error
}

// NewComposeRunner returns a runner that shells out to the real docker CLI,
// carrying opts through the CLI's environment so it targets the same daemon the
// SDK-backed Engine does.
func NewComposeRunner(opts Options) *ComposeRunner {
	return &ComposeRunner{
		run: newRunner(opts),
	}
}

// Stop stops all project services. docker compose stop only returns once the
// containers have exited, so no extra polling is needed.
func (c *ComposeRunner) Stop(ctx context.Context, files ...string) error {
	return c.run(ctx, composeArgs(files, "stop")...)
}

// Up starts all project services in the background and waits until they are
// healthy, honouring container health checks where defined (--wait).
//
// waitTimeoutSec bounds how long it blocks on health checks: without a bound, a
// service that never becomes healthy (e.g. a crash-looping container with
// restart: always) would make Up hang forever, so the bound turns that into an
// error callers can act on (e.g. rollback reverts to its safety backup). A
// value <= 0 falls back to DefaultUpWaitTimeout.
func (c *ComposeRunner) Up(ctx context.Context, waitTimeoutSec int, files ...string) error {
	if waitTimeoutSec <= 0 {
		waitTimeoutSec = DefaultUpWaitTimeout
	}
	timeout := strconv.Itoa(waitTimeoutSec)
	return c.run(ctx, composeArgs(files, "up", "-d", "--wait", "--wait-timeout", timeout)...)
}

// composeArgs assembles `compose -f <file> … <args…>`, the prefix every
// ComposeRunner invocation shares.
func composeArgs(files []string, args ...string) []string {
	out := []string{"compose"}
	for _, f := range files {
		out = append(out, "-f", f)
	}
	return append(out, args...)
}
