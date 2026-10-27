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

// Package eraser carries out the gc's actions, one GarbageEraser per action.
package eraser

import (
	"context"
	"fmt"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/gc"
)

// ObjectEraser deletes one object per op.
type ObjectEraser struct{}

func NewObjectEraser() ObjectEraser { return ObjectEraser{} }

func (s ObjectEraser) Action() model.GarbageAction { return model.GarbageActionDeleteObject }

// Erase deletes every object ops names, one at a time, since a batch Delete is refused whole for
// one object already gone and fails whole for one that cannot be removed.
func (s ObjectEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return store.Delete(ctx, op.Target)
	})
}

// TreeEraser deletes one directory per op, whole.
type TreeEraser struct{}

func NewTreeEraser() TreeEraser { return TreeEraser{} }

func (s TreeEraser) Action() model.GarbageAction { return model.GarbageActionDeleteTree }

// Erase deletes every directory ops names, whole.
func (s TreeEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return store.DeleteAll(ctx, op.Target)
	})
}

// ImageEraser deletes one stored image per op.
type ImageEraser struct{}

func NewImageEraser() ImageEraser { return ImageEraser{} }

func (s ImageEraser) Action() model.GarbageAction { return model.GarbageActionDeleteImage }

// Erase deletes every image directory ops names, each one's manifest first.
func (s ImageEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return imagecopy.DeleteStoredImage(ctx, store, op.Target)
	})
}

// Pruner forgets snapshots in one compose project's restic repositories.
type Pruner interface {
	// Forget forgets ids in objectID's repository and prunes it, returning what the prune freed.
	Forget(ctx context.Context, objectID string, ids []string) (*int64, error)
	// Close releases what Open started.
	Close(ctx context.Context)
}

// PrunerOpener opens the Pruner of one compose project's store.
type PrunerOpener interface {
	Open(ctx context.Context, store storages.Navigator) (Pruner, error)
}

// ForgetEraser forgets restic snapshots, one forget --prune per repository.
type ForgetEraser struct {
	opener PrunerOpener
}

func NewForgetEraser(opener PrunerOpener) ForgetEraser {
	return ForgetEraser{opener: opener}
}

func (s ForgetEraser) Action() model.GarbageAction { return model.GarbageActionForget }

// Erase forgets the snapshots ops name, repository by repository in the order each first appears,
// pruning what they alone held; a repository that fails fails each of its ops. A snapshot frees
// nothing itself: what its repository's prune freed is reclaimed.
func (s ForgetEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	pruner, err := s.opener.Open(ctx, store)
	if err != nil {
		return model.GarbageDeletion{Failed: newFailures(ops, fmt.Errorf("open the restic repositories: %w", err))}
	}
	defer pruner.Close(ctx)

	var (
		repos, failed = newRepoForgets(ops)
		deleted       []model.GarbageDeleted
		reclaimed     []model.GarbageReclaimed
		notRun        int
	)
	for i, repo := range repos {
		if ctx.Err() != nil {
			notRun = countOps(repos[i:])
			break
		}
		freed, err := pruner.Forget(ctx, repo.objectID, repo.ids)
		if err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("repository", repo.objectID).Msg("forget dead snapshots")
			failed = append(failed, newFailures(repo.ops, err)...)
			continue
		}
		for _, op := range repo.ops {
			deleted = append(deleted, model.GarbageDeleted{
				Target: op.Target,
				Bytes:  new(int64),
			})
		}
		reclaimed = append(reclaimed, model.GarbageReclaimed{
			Target: cdc.NewRepositoryKey(repo.objectID),
			Bytes:  freed,
		})
	}
	return model.GarbageDeletion{
		Deleted:   deleted,
		Failed:    failed,
		NotRun:    notRun,
		Reclaimed: reclaimed,
	}
}

// repoForget is the snapshots of one repository ops name.
type repoForget struct {
	objectID string
	ids      []string
	ops      []model.GarbageOp
}

// newRepoForgets groups ops by repository, in the order each first appears; an op whose target is
// no snapshot key fails.
func newRepoForgets(ops []model.GarbageOp) ([]repoForget, []model.GarbageDeleteFailure) {
	var (
		repos  []repoForget
		failed []model.GarbageDeleteFailure
		byRepo = map[string]int{}
	)
	for _, op := range ops {
		objectID, id, err := cdc.ParseSnapshotKey(op.Target)
		if err != nil {
			failed = append(failed, model.GarbageDeleteFailure{
				Target: op.Target,
				Error:  err.Error(),
			})
			continue
		}
		i, ok := byRepo[objectID]
		if !ok {
			i = len(repos)
			byRepo[objectID] = i
			repos = append(repos, repoForget{objectID: objectID})
		}
		repos[i].ids = append(repos[i].ids, id)
		repos[i].ops = append(repos[i].ops, op)
	}
	return repos, failed
}

// countOps counts the ops of repos.
func countOps(repos []repoForget) int {
	var n int
	for _, repo := range repos {
		n += len(repo.ops)
	}
	return n
}

// newFailures fails every op of ops with err.
func newFailures(ops []model.GarbageOp, err error) []model.GarbageDeleteFailure {
	failed := make([]model.GarbageDeleteFailure, 0, len(ops))
	for _, op := range ops {
		failed = append(failed, model.GarbageDeleteFailure{
			Target: op.Target,
			Error:  err.Error(),
		})
	}
	return failed
}

// tagRemover is the daemon as TagEraser takes a reference off it.
type tagRemover interface {
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// TagEraser takes restored layer tags off the daemon, one per op.
type TagEraser struct {
	daemon tagRemover
}

func NewTagEraser(daemon tagRemover) TagEraser {
	return TagEraser{daemon: daemon}
}

func (s TagEraser) Action() model.GarbageAction { return model.GarbageActionUntag }

// Erase takes every tag ops name off the daemon, unforced: a tag a container was created from
// since it was found fails alone.
func (s TagEraser) Erase(ctx context.Context, ops []model.GarbageOp) model.GarbageDeletion {
	return gc.DeleteOps(ctx, ops, func(ctx context.Context, op model.GarbageOp) error {
		return s.daemon.RemoveImageRef(ctx, model.ImageRef(op.Target))
	})
}
