package storefactory

import (
	"context"
	"errors"
	"fmt"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
)

// The link between the two halves of the seam, asserted at compile time rather
// than discovered at wiring: a provider is handed this type through the narrow
// view, and arcave keeps the two methods that view does not carry.
var _ provider.Stores = (*Stores)(nil)

// Stores is the stack root seen through the scopes each side of the provider seam
// may address. One type, two audiences, and the split is the whole ownership
// story:
//
//   - a provider receives it as provider.Stores and can reach exactly two
//     things — the tree of one savepoint, and a named store that outlives every
//     savepoint of the session;
//   - arcave keeps IsEmpty and Sweep, which address a savepoint's tree *from
//     outside*. They cannot be provider methods, and not merely by policy: a
//     Storager may not delete its own root, so sweeping a savepoint directory is
//     only possible from the parent — the very root a provider is never handed.
//
// It holds the root rather than a path, so nothing here names a directory: New
// already resolved that, and this type only re-roots.
type Stores struct {
	root storages.Storager
}

// NewStores wraps an already-rooted stack store — what New returns. It opens
// nothing: every method below is a re-rooting or one object call.
func NewStores(root storages.Storager) *Stores {
	return &Stores{root: root}
}

// Savepoint returns the tree one savepoint owns, for the artifacts that die with
// it. The id is gated here because this is where it becomes a key — the third of
// the three depths that call SavepointID.Validate, beside the service and the
// savepoint repository.
//
// The prefix is created on demand (the true), so a capture that writes nothing
// leaves no empty directory behind for IsEmpty to trip over on a retry.
func (s *Stores) Savepoint(id model.SavepointID) (storages.Storager, error) {
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("savepoint scope: %w", err)
	}
	store, err := s.root.SubStorage(id.String(), true)
	if err != nil {
		return nil, fmt.Errorf("open savepoint scope %s: %w", id, err)
	}
	return store, nil
}

// Session returns a store that outlives every savepoint of the session — the
// scope a deduplicating store lives in, `restic` today.
//
// It sits *beside* the savepoint trees rather than under one, and that placement
// is the whole value of the scope: a repository that died with its savepoint
// would make every capture pay a cold full backup, which is the one thing cdc
// exists to avoid. Nothing arcave does deletes here.
func (s *Stores) Session(namespace model.StoreNamespace) (storages.Storager, error) {
	if err := namespace.Validate(); err != nil {
		return nil, fmt.Errorf("session scope: %w", err)
	}
	store, err := s.root.SubStorage(namespace.String(), true)
	if err != nil {
		return nil, fmt.Errorf("open session scope %s: %w", namespace, err)
	}
	return store, nil
}

// IsEmpty reports whether the tree id names holds nothing yet. arcave's, not a
// provider's: only an interrupted run can have left objects there — a committed
// savepoint's id is never created twice — and those objects are the last copy of
// whatever that run captured, so a create refuses rather than sweeps.
func (s *Stores) IsEmpty(ctx context.Context, id model.SavepointID) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	objects, err := s.root.List(ctx, id.String())
	if err != nil {
		return false, fmt.Errorf("read the savepoint tree %s: %w", id, err)
	}
	return len(objects) == 0, nil
}

// Sweep drops the whole tree id names — the one storage key arcave addresses
// itself; everything below it belongs to the provider that wrote it. An absent
// tree is not an error, so a sweep after a capture that never wrote anything is
// a no-op rather than a second failure on a failure path.
func (s *Stores) Sweep(ctx context.Context, id model.SavepointID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := s.root.DeleteAll(ctx, id.String()); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("remove the savepoint tree %s: %w", id, err)
	}
	return nil
}
