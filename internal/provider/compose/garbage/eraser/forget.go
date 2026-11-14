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

package eraser

import (
	"context"
	"fmt"
	"sync"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/garbage"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
)

// Pruner forgets snapshots in one compose project's restic repositories.
type Pruner interface {
	// Forget forgets ids in objectID's repository and prunes it, or only prunes it when ids is
	// empty, returning what the prune freed.
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
	opener      PrunerOpener
	concurrency int
}

// NewForgetEraser prunes at most concurrency repositories at once.
func NewForgetEraser(opener PrunerOpener, concurrency int) ForgetEraser {
	return ForgetEraser{
		opener:      opener,
		concurrency: concurrency,
	}
}

func (s ForgetEraser) Action() model.GarbageAction { return model.GarbageActionForget }

// Erase forgets the snapshots ops name, at most s.concurrency repositories at once, pruning what
// they alone held; an op naming a repository prunes it alone. A repository that fails fails each
// of its ops. A snapshot frees nothing itself: what its repository's prune freed is reclaimed.
func (s ForgetEraser) Erase(
	ctx context.Context, store storages.Storager, ops []model.GarbageOp,
) model.GarbageDeletion {
	pruner, err := s.opener.Open(ctx, store)
	if err != nil {
		return model.GarbageDeletion{Failed: newFailures(ops, fmt.Errorf("open the restic repositories: %w", err), ctx.Err() != nil)}
	}
	defer pruner.Close(ctx)

	var (
		repos, failed = newRepoForgets(ops)
		pruned        = make([]repoPrune, len(repos))
		wg            sync.WaitGroup
		slots         = make(chan struct{}, max(s.concurrency, 1))
	)
	ctx, batch := garbage.BeginDeletion(ctx, len(repos))
	for i, repo := range repos {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			pruned[i] = forgetRepo(ctx, pruner, repo)
		})
	}
	wg.Wait()
	deletion := newForgetDeletion(repos, pruned, failed)
	garbage.EndDeletion(ctx, batch, deletion)
	return deletion
}

// repoForget is the snapshots of one repository ops name; ids is empty for a prune alone.
type repoForget struct {
	objectID string
	ids      []string
	ops      []model.GarbageOp
}

// newRepoForgets groups ops by repository, in the order each first appears; an op whose target is
// neither a snapshot's key nor a repository's fails.
func newRepoForgets(ops []model.GarbageOp) ([]repoForget, []model.GarbageDeleteFailure) {
	var (
		order   []string
		ids     = map[string][]string{}
		repoOps = map[string][]model.GarbageOp{}
		failed  []model.GarbageDeleteFailure
	)
	for _, op := range ops {
		objectID, id, err := parseForgetTarget(op.Target)
		if err != nil {
			failed = append(failed, model.GarbageDeleteFailure{
				Target: op.Target,
				Error:  err.Error(),
			})
			continue
		}
		if _, seen := repoOps[objectID]; !seen {
			order = append(order, objectID)
		}
		if id != "" {
			ids[objectID] = append(ids[objectID], id)
		}
		repoOps[objectID] = append(repoOps[objectID], op)
	}
	repos := make([]repoForget, 0, len(order))
	for _, objectID := range order {
		repos = append(repos, repoForget{
			objectID: objectID,
			ids:      ids[objectID],
			ops:      repoOps[objectID],
		})
	}
	return repos, failed
}

// parseForgetTarget splits a forget op's target into its repository and snapshot id, the id empty
// for a target naming the repository alone.
func parseForgetTarget(target string) (objectID, id string, err error) {
	if objectID, err := cdc.ParseRepositoryKey(target); err == nil {
		return objectID, "", nil
	}
	return cdc.ParseSnapshotKey(target)
}

// repoPrune is what forgetting one repository's snapshots did.
type repoPrune struct {
	freed *int64
	err   error
	// notRun is a repository the run's end came before.
	notRun bool
	// interrupted is a failure the run's end came with.
	interrupted bool
}

// forgetRepo forgets repo's snapshots, unless ctx is done.
func forgetRepo(ctx context.Context, pruner Pruner, repo repoForget) repoPrune {
	if ctx.Err() != nil {
		return repoPrune{notRun: true}
	}
	repoCtx, step := garbage.BeginTarget(ctx, cdc.NewRepositoryKey(repo.objectID))
	freed, err := pruner.Forget(repoCtx, repo.objectID, repo.ids)
	if err != nil {
		step.End(err)
		zerolog.Ctx(ctx).Error().Err(err).Str("repository", repo.objectID).Msg("forget dead snapshots")
		return repoPrune{
			err:         err,
			interrupted: ctx.Err() != nil,
		}
	}
	step.Done(garbage.NewFreedSample(freed))
	return repoPrune{freed: freed}
}

// newForgetDeletion is what pruning repos did, pruned[i] being repos[i]'s, after failed.
func newForgetDeletion(
	repos []repoForget, pruned []repoPrune, failed []model.GarbageDeleteFailure,
) model.GarbageDeletion {
	var (
		deleted   []model.GarbageDeleted
		reclaimed []model.GarbageReclaimed
		notRun    int
	)
	for i, repo := range repos {
		switch result := pruned[i]; {
		case result.notRun:
			notRun += len(repo.ops)
		case result.err != nil:
			failed = append(failed, newFailures(repo.ops, result.err, result.interrupted)...)
		default:
			for _, op := range repo.ops {
				deleted = append(deleted, model.GarbageDeleted{
					Target: op.Target,
					Bytes:  new(int64),
				})
			}
			reclaimed = append(reclaimed, model.GarbageReclaimed{
				Target: cdc.NewRepositoryKey(repo.objectID),
				Bytes:  result.freed,
			})
		}
	}
	return model.GarbageDeletion{
		Deleted:   deleted,
		Failed:    failed,
		NotRun:    notRun,
		Reclaimed: reclaimed,
	}
}

// newFailures fails every op of ops with err, interrupted when the run's end came with it.
func newFailures(ops []model.GarbageOp, err error, interrupted bool) []model.GarbageDeleteFailure {
	failed := make([]model.GarbageDeleteFailure, 0, len(ops))
	for _, op := range ops {
		failed = append(failed, model.GarbageDeleteFailure{
			Target:      op.Target,
			Error:       err.Error(),
			Interrupted: interrupted,
		})
	}
	return failed
}
