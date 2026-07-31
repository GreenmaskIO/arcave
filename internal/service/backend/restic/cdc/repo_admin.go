package cdc

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

// The restic repository objects arcave addresses directly.
const (
	configKey     = "config"
	locksPrefix   = "locks"
	snapshotsPath = "snapshots"
)

// repoAdmin is the server-side half of the backend: what can be answered or
// repaired by addressing the repository's objects, without a restic run. Its
// store is already rooted at one repository.
type repoAdmin struct {
	store storages.Storager
}

// HasConfig reports whether the repository has been initialised: a repository
// without a config object is one restic has never seen.
func (a repoAdmin) HasConfig(ctx context.Context) (bool, error) {
	exists, err := a.store.Exists(ctx, configKey)
	if err != nil {
		return false, fmt.Errorf("stat repository config: %w", err)
	}
	return exists, nil
}

// ClearLocks deletes every object under locks/, which `restic unlock` cannot
// do: a lock written inside a container carries that container's hostname, so
// the next container sees a live lock held by a stranger and only the 30-minute
// age quarantine would clear it.
//
// Safe because arcave holds the project flock for the whole workflow and the
// repository has no other client, so any lock standing at the start of an
// operation is an orphan. Only at the start — restic refreshes its own lock
// while it works.
func (a repoAdmin) ClearLocks(ctx context.Context) error {
	locks, err := a.store.List(ctx, locksPrefix)
	if err != nil {
		return fmt.Errorf("list repository locks: %w", err)
	}
	for _, lock := range locks {
		key := path.Join(locksPrefix, lock.Name)
		// List and Delete are two calls: a lock already gone is fine.
		if err := a.store.Delete(ctx, key); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
			return fmt.Errorf("delete stale repository lock %s: %w", key, err)
		}
		zerolog.Ctx(ctx).Debug().Str("lock", key).Msg("cleared a stale restic lock")
	}
	return nil
}

// DeleteSnapshot drops one snapshot by id — what `restic forget <id>` does on
// the wire, without the container or its exclusive lock. A snapshot already gone
// is not an error. It reclaims no space: the chunks it referenced are shared,
// and only a prune returns bytes.
func (a repoAdmin) DeleteSnapshot(ctx context.Context, snapshotRef string) error {
	if err := validateSnapshotRef(snapshotRef); err != nil {
		return err
	}
	key := path.Join(snapshotsPath, snapshotRef)
	if err := a.store.Delete(ctx, key); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete snapshot %s: %w", snapshotRef, err)
	}
	return nil
}
