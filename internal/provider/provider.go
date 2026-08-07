// Package provider declares the half of arcave a provider is allowed to touch.
//
// arcave owns the atomic operation — the savepoint id, the record that commits
// it, the project lock, the interrupted-operation marker and retention. A
// provider owns everything else: how a stack is brought to rest, what its units
// of state are, and how their bytes are captured and put back. arcave waits for
// a result the provider can guarantee, or an error, and never learns which of
// those things happened inside.
//
// This package is what crosses that line in the other direction, and it is
// deliberately tiny: two scopes of object storage. No path, no arcaved endpoint,
// no server object, no credential.
package provider

import (
	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// Stores is arcave's storage lent to a provider, in the only two scopes a
// savepoint needs. A provider asks for the scope it wants; arcave never pushes
// one, and every Storager arrives already rooted — no path crosses this seam.
//
// It is a seam rather than a value so one provider works unchanged on both sides
// of a process boundary. In-process these are arcave's own Storagers. Out of
// process the provider is handed a scope grant instead — a base URL, a prefix
// and a short-lived token — and builds a remote arm of storages.Storager from
// it, holding no store credentials at all. Nothing in the provider changes
// between the two, which is the whole point of keeping the contract at
// storages.Storager rather than inventing an arcave RPC: that interface already
// has remote arms (s3, azure, ssh), so an arm over arcave's own API is one more
// of a kind that exists.
type Stores interface {
	// Savepoint is a namespace of its own per savepoint, for artifacts that
	// belong to exactly one savepoint and die with it — an archive, a manifest,
	// a committed image layer.
	//
	// arcave sweeps this tree when the savepoint is removed, so a provider must
	// keep nothing here that has to outlive one savepoint. The reserved key at
	// its root (savepoint.json) is arcave's; a provider writes below a segment of
	// its own.
	Savepoint(id model.SavepointID) (storages.Storager, error)

	// Session outlives every savepoint of the session, for stores that
	// deduplicate across time: a restic repository per volume, a CAS of image
	// layers. namespace names one such store.
	//
	// arcave never deletes anything here on a savepoint's behalf — it cannot know
	// what a dedup store still needs. Reclaiming is the provider's, through
	// Discard.
	Session(namespace model.StoreNamespace) (storages.Storager, error)
}
