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
	"fmt"

	"github.com/greenmaskio/storages"
)

// Pruner forgets snapshots and prunes the data they alone held, under restic's own lock. It clears
// a repository's locks first, as a capture does: the gc it runs for holds the session's lock.
type Pruner struct {
	repositories repositories
	store        storages.Navigator
}

// NewPruner runs restic through runner against the repositories repoURLBase addresses; store is
// rooted at NamespaceRepos, the same repositories as objects.
func NewPruner(runner ContainerRunner, store storages.Navigator, repoURLBase string, options Options) *Pruner {
	return &Pruner{
		repositories: newRepositories(runner, repoURLBase, options),
		store:        store,
	}
}

// Forget forgets ids in objectID's repository and prunes it, or only prunes it when ids is empty;
// it returns what the prune freed, nil when restic printed no total.
func (s *Pruner) Forget(ctx context.Context, objectID string, ids []string) (*int64, error) {
	args, err := newPruneArgs(ids)
	if err != nil {
		return nil, err
	}
	resticRunner, err := s.repositories.newRunner(objectID)
	if err != nil {
		return nil, err
	}
	repoStore, err := s.store.SubStorage(objectID, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at %s: %w", objectID, err)
	}
	if err := (&objectRepo{store: repoStore}).ClearLocks(ctx); err != nil {
		return nil, err
	}
	out, err := resticRunner.output(ctx, resticCmd{args: args})
	if err != nil {
		return nil, err
	}
	return readPruneTotal(ctx, out, objectID)
}
