// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cdc

import (
	"context"
	"errors"
	"fmt"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

// The restic repository objects arcave addresses directly.
const (
	configKey     = "config"
	locksPrefix   = "locks"
	snapshotsPath = "snapshots"
)

// objectRepo is the server-side half of the backend: what can be answered or
// repaired by addressing the repository's objects, without a restic run. Its
// store is already rooted at one repository.
type objectRepo struct {
	store storages.Storager
}

// HasConfig reports whether the repository has been initialised: a repository
// without a config object is one restic has never seen.
func (r *objectRepo) HasConfig(ctx context.Context) (bool, error) {
	exists, err := r.store.Exists(ctx, configKey)
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
func (r *objectRepo) ClearLocks(ctx context.Context) error {
	// A repository without locks/ has nothing to clear.
	if err := r.store.DeleteAll(ctx, locksPrefix); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete stale repository locks: %w", err)
	}
	zerolog.Ctx(ctx).Debug().Msg("cleared stale restic locks")
	return nil
}

// DeleteSnapshot drops one snapshot by id — what `restic forget <id>` does on
// the wire, without the container or its exclusive lock. A snapshot already gone
// is not an error. It reclaims no space: the chunks it referenced are shared,
// and only a prune returns bytes.
//
// The delete goes through a store rooted at snapshots/ rather than a key joined
// onto it, and that is what keeps the id safe without arcave judging its shape:
// "../config" escapes that root and the store's own guard refuses it, where the
// same id joined onto the repository root would have stayed inside it and
// deleted the object the repository is.
func (r *objectRepo) DeleteSnapshot(ctx context.Context, snapshotObjectID string) error {
	if snapshotObjectID == "" {
		return errNoSnapshotID
	}
	snapshots, err := r.store.SubStorage(snapshotsPath, true)
	if err != nil {
		return fmt.Errorf("re-root store at snapshots: %w", err)
	}
	if err := snapshots.Delete(ctx, snapshotObjectID); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete snapshot %s: %w", snapshotObjectID, err)
	}
	return nil
}
