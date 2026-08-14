// Package provider declares the seam arcave drives a stateful environment
// through. arcave knows nothing of a call's inside; it keeps only the
// transaction around it.
package provider

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

// StackProvider is one stack runtime. It holds no savepoint identity: one
// transaction addresses two, so the id arrives per call.
type StackProvider interface {
	// Kind is what the record and the store layout name this provider by.
	Kind() model.ProviderKind

	// Capture brings the stack to a consistent state and reports one artifact per
	// unit. Atomic: on error arcave commits nothing, and the restart is the provider's.
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

	// Close releases what the provider holds for its own lifetime — a daemon
	// connection, a progress writer. Whoever built it closes it.
	Close(ctx context.Context) error
}

// Factory builds one provider from its own configuration, which arcave carries
// and never reads. A registry is a table of these, keyed by the type token a
// config entry names.
type Factory func(ctx context.Context, settings model.RawSettings) (StackProvider, error)

// CaptureRequest is what arcave asks for. It carries the identity and the marks,
// and nothing about how.
type CaptureRequest struct {
	// SavepointID is both the identity and the name of the savepoint scope. It is
	// validated before it reaches here, and again by the provider, which is where
	// it becomes a store key.
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
	Artifacts []model.Artifact

	// Skipped is what the provider deliberately left out of the capture, with a
	// reason per entry. arcave carries it to the adapters, which decide how
	// loudly to render it; an empty capture is the provider's own error, not an
	// empty Skipped.
	Skipped []model.SkippedUnit
}

// RestoreRequest names the savepoint to put back and hands its artifacts along:
// the provider addresses its own captures, so arcave never rebuilds a key.
type RestoreRequest struct {
	SavepointID model.SavepointID
	Artifacts   []model.Artifact
}

// DiscardRequest names a savepoint arcave has already uncommitted.
type DiscardRequest struct {
	SavepointID model.SavepointID
	Artifacts   []model.Artifact
}
