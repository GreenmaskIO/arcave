// Package sandbox is the named hole for the isolation-boundary provider: the
// level "Песочница" of the three-level model, between the host and the stack.
//
// Nothing here is built. It exists so the shape is visible while the stack
// provider is cut, and because the store layout already reserves the namespace
// (`sandbox/` beside `compose/`). The arm that ships first is `host` — "there is
// no sandbox", today's local mode — and it stays first-class: under it, today's
// `arcave savepoint create` is a special case of the general scheme, not a
// separate branch.
//
// Deliberately NOT on this seam, and the reason matters:
//
//   - Capture/Restore of a sandbox's own parts (workspace, writable layer, agent
//     context) go through the same stack.Provider contract, because they are the
//     same operation over different units — a sandbox savepoint is arcave
//     composing two providers under one id, joined by a pin. Duplicating the
//     verbs here would duplicate the capture mechanics too, which is exactly what
//     the cut is meant to avoid.
//   - Freeze of the agent's docker access is not a provider verb at all: it is a
//     lease on the arcaved proxy, held for the length of one operation and dying
//     with its holder.
//   - Pause belongs here and only here. It is refused for stack services —
//     a snapshot taken from under `docker pause` is crash-state, and the whole
//     value of stop-first is the application consistency that graceful shutdown
//     buys. A sandbox is paused as a whole, and only after its stack was already
//     captured: pausing it first would freeze the nested arcaved that has to do
//     the capturing.
package sandbox

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

// Provider is the lifecycle of one isolation boundary. It is not a savepoint
// seam: what a sandbox savepoint captures goes through stack.Provider, and this
// interface only creates the boundary, pauses it for the length of a capture, and
// says how the level below reaches its runtime.
//
// Unimplemented. The method set is a sketch against the ownership table of the
// decomposition report, not a contract — see .user/knowledge/arcave.
type Provider interface {
	// Kind is what the store layout and the config name this provider by.
	Kind() model.ProviderKind

	// Up creates the boundary and everything that lives for it: its volumes, its
	// network, its nested arcaved. Idempotent on an already-running sandbox.
	Up(ctx context.Context) error

	// Down tears it down as a cascade — the nested stack stopped gracefully
	// first, then the nested arcaved, then the container. Killing the container
	// first kills the nested arcaved with every container under it and no graceful
	// shutdown at all, which leaves a database in crash-state for the next
	// savepoint to inherit.
	Down(ctx context.Context) error

	// Pause freezes every process inside for the length of a capture, and Resume
	// releases it. Only legal after the nested stack has already been captured:
	// a paused sandbox has no live arcaved to capture through.
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error

	// Runtime is how the level below reaches its own arcaved — the endpoint a
	// stack provider inside this sandbox is built over. This is the late-bound
	// value of the cut: in the `host` arm it is the configured local arcaved, in
	// the DinD arm the nested one, and neither is known from config alone.
	//
	// It is deliberately opaque here: arcave carries it from one provider to the
	// other and never interprets it.
	Runtime(ctx context.Context) (Runtime, error)
}

// Runtime is what a sandbox hands the stack provider that runs inside it. arcave
// passes it through without reading it — which arcaved a stack lives on is a fact
// two providers share and arcave only routes.
type Runtime struct {
	// Endpoint addresses the container runtime, in whatever form its provider
	// speaks (a docker host, a socket path, a context name).
	Endpoint string
	// ContainerHost is how a container on that runtime names the host a
	// provider's own server listens on. It is here rather than in arcave's config
	// because the answer differs per boundary — host.docker.internal on Docker
	// Desktop, a gateway address inside DinD, host.containers.internal on podman.
	ContainerHost string
}
