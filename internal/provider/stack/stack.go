// Package stack declares the seam arcave drives a stateful stack through — a
// docker-compose project today, a podman-compose or k8s one later.
//
// Three atomic operations and one reading one. arcave knows nothing of what
// happens inside a call: whether the provider stops containers, freezes them or
// clones volumes is the provider's business, and the epoch it promises is the
// provider's promise to keep. What arcave keeps is the transaction around the
// call — see internal/service/arcaved.
//
// The package holds no implementation. It names the contract, and the data it
// speaks lives in internal/model, because the savepoint record carries it: an
// artifact is written into savepoint.json and handed back verbatim on the way
// out.
package stack

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

// Provider is one stack runtime, bound at construction to the provider.Stores
// arcave lends it and to whatever reaches that runtime — a compose client, a
// docker engine, its own capture backends, its own restic server if it wants
// one. None of that crosses this seam.
//
// It holds no savepoint identity: the id arrives per call, because one arcave
// transaction addresses two savepoints — the one it restores and the undo it
// takes first.
type Provider interface {
	// Kind is what the record and the store layout name this provider by.
	Kind() model.ProviderKind

	// Capture brings the stack to a consistent state, writes whatever it needs
	// through provider.Stores, and reports one artifact per unit it covered.
	//
	// Atomic: on error arcave commits nothing, so the savepoint never existed.
	// Restarting the stack after a failed capture is the provider's — arcave has
	// no verb for it and must not grow one.
	Capture(ctx context.Context, req CaptureRequest) (CaptureResult, error)

	// Restore puts the stack at the artifacts of one savepoint. Atomic in the
	// same sense: either the stack is there, or the error says it is where it
	// was. arcave's interrupted-operation marker stands across the whole call, so
	// a run killed inside it is visible to the next command's gate.
	Restore(ctx context.Context, req RestoreRequest) error

	// Discard releases what the provider holds for a savepoint arcave has already
	// uncommitted — a restic snapshot, an image blob. Everything under the
	// savepoint scope is arcave's to sweep, so this is for the session scope
	// alone.
	//
	// An artifact the provider cannot reach is reported, never refused: the
	// record is already gone and there is nothing left to protect.
	Discard(ctx context.Context, req DiscardRequest) error
}

// Inspector is the reading half — the layout of the stack as declared, which
// `arcave recommend` and a future `arcave status` read.
//
// A seam of its own rather than a method on Provider, because no savepoint
// workflow calls it: a read-only command must not have to build a capture
// runtime to answer a question about a file.
type Inspector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// CaptureRequest is what arcave asks for. It carries the identity and the marks,
// and nothing about how.
type CaptureRequest struct {
	// SavepointID is both the identity and the name of the Savepoint scope. It is
	// validated before it reaches here; provider.Stores validates it again, being
	// the place it becomes a key.
	SavepointID model.SavepointID

	// Labels are arcave's marks — a rollback's undo carries
	// model.SafetySnapshotLabel among them — for the provider to render into
	// whatever marking its own stores understand, so a capture an interrupted run
	// abandoned is findable with the store's native tooling.
	//
	// A provider with nowhere to put them ignores them, and none is ever read
	// back: arcave reads its own record, never the provider's marking.
	Labels map[string]string
}

// CaptureResult is what the provider guarantees it wrote.
type CaptureResult struct {
	// Artifacts is what arcave writes into the record, and what Restore and
	// Discard are handed back verbatim. Only the provider that produced one
	// interprets it — arcave carries it.
	//
	// It must be JSON-round-trippable: it goes through savepoint.json, and it
	// will go over a wire the day the provider is a service.
	Artifacts []model.SnapshotStat

	// Skipped is what the provider deliberately left out of the capture, with a
	// reason per entry. arcave carries it to the adapters, which decide how
	// loudly to render it; an empty capture is the provider's own error, not an
	// empty Skipped.
	Skipped []model.SkippedVolume
}

// RestoreRequest names the savepoint to put back and hands its artifacts along:
// the provider addresses its own captures, so arcave never rebuilds a key.
type RestoreRequest struct {
	SavepointID model.SavepointID
	Artifacts   []model.SnapshotStat
}

// DiscardRequest names a savepoint arcave has already uncommitted.
type DiscardRequest struct {
	SavepointID model.SavepointID
	Artifacts   []model.SnapshotStat
}
