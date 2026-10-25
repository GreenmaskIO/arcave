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

// Package resticrepo is the restic-repositories collector: cdc snapshots whose savepoint is dead.
package resticrepo

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/gc"
)

// probeTimeout bounds the check that the daemon answers.
const probeTimeout = 10 * time.Second

// Pinger is the daemon restic runs in.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Inspector reads one compose project's restic repositories through restic.
type Inspector interface {
	ReadSnapshots(ctx context.Context, objectID string) ([]cdc.Snapshot, error)
	EstimateForget(ctx context.Context, objectID string, ids []string) (*int64, error)
	// Close releases what Open started.
	Close(ctx context.Context)
}

// InspectorOpener opens the Inspector of one compose project's store.
type InspectorOpener interface {
	Open(ctx context.Context, store storages.Navigator) (Inspector, error)
}

// Collector finds the cdc snapshots whose savepoint is dead, by restic's own listing and the
// arcave-savepoint tag every capture writes; restic estimates what forgetting them frees.
type Collector struct {
	daemon Pinger
	opener InspectorOpener
}

// New reads the repositories opener opens, in containers of daemon.
func New(daemon Pinger, opener InspectorOpener) *Collector {
	return &Collector{
		daemon: daemon,
		opener: opener,
	}
}

func (s *Collector) Kind() model.GarbageCollectorKind {
	return model.GarbageCollectorKindResticRepositories
}

func (s *Collector) DataKind() model.GarbageDataKind {
	return model.GarbageDataKindDeduplicatedVolumes
}

// Probe checks, within probeTimeout, that the daemon answers.
func (s *Collector) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return s.daemon.Ping(ctx)
}

// Find judges every snapshot of every repository of the scope by its savepoint. A repository
// restic cannot list is unjudged; the others are still judged.
func (s *Collector) Find(ctx context.Context, scope gc.ComposeProjectScope) (model.GarbageStat, error) {
	repos, err := cdc.ListRepositories(ctx, scope.Store)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, nil
	}
	inspector, err := s.opener.Open(ctx, scope.Store)
	if err != nil {
		return nil, fmt.Errorf("open the restic repositories: %w", err)
	}
	defer inspector.Close(ctx)

	var (
		committed                = newCommittedSavepoints(scope)
		ops                      = []model.GarbageOp{}
		unjudged                 []model.GarbageSkip
		estimates                []model.ResticForgetEstimate
		liveRepos, liveSnapshots int
		unattributed             int
	)
	for _, repo := range repos {
		key := path.Join(cdc.NamespaceRepos, repo)
		snapshots, err := inspector.ReadSnapshots(ctx, repo)
		if err != nil {
			unjudged = append(unjudged, model.GarbageSkip{
				Target: key,
				Reason: model.GarbageReasonBackendUnreadable,
				Error:  err.Error(),
			})
			continue
		}
		verdict := judgeSnapshots(committed, key, snapshots)
		ops = append(ops, verdict.ops...)
		liveSnapshots += verdict.live
		unattributed += verdict.unattributed
		if verdict.live > 0 {
			liveRepos++
		}
		if len(verdict.dead) > 0 {
			estimates = append(estimates, model.ResticForgetEstimate{
				Repo:       key,
				Snapshots:  verdict.dead,
				FreedBytes: readEstimate(ctx, inspector, repo, verdict.dead),
			})
		}
	}
	return model.ResticRepositoryStat{
		GarbageFindings: model.GarbageFindings{
			Ops:      ops,
			Unjudged: unjudged,
		},
		LiveRepos:     liveRepos,
		LiveSnapshots: liveSnapshots,
		Unattributed:  unattributed,
		Estimates:     estimates,
	}, nil
}

// newCommittedSavepoints is the set of savepoints the scope's records commit: a snapshot of any
// other is dead.
func newCommittedSavepoints(scope gc.ComposeProjectScope) map[model.SavepointID]bool {
	committed := map[model.SavepointID]bool{}
	for _, savepoint := range scope.Savepoints {
		committed[savepoint.ID] = true
	}
	return committed
}

// repoVerdict is what judging one repository's snapshots found; dead are the ids of ops.
type repoVerdict struct {
	live, unattributed int
	ops                []model.GarbageOp
	dead               []string
}

// judgeSnapshots judges every snapshot of the repository at key by its savepoint.
func judgeSnapshots(committed map[model.SavepointID]bool, key string, snapshots []cdc.Snapshot) repoVerdict {
	var (
		live, unattributed int
		ops                []model.GarbageOp
		dead               []string
	)
	for _, snapshot := range snapshots {
		switch {
		case snapshot.Savepoint == "":
			unattributed++
		case committed[snapshot.Savepoint]:
			live++
		default:
			dead = append(dead, snapshot.ID)
			ops = append(ops, model.GarbageOp{
				Action:       model.GarbageActionForget,
				Target:       path.Join(key, "snapshots", snapshot.ID),
				Reason:       model.GarbageReasonDeadCapture,
				LastModified: snapshot.Time,
				Savepoint:    snapshot.Savepoint,
			})
		}
	}
	return repoVerdict{
		live:         live,
		unattributed: unattributed,
		ops:          ops,
		dead:         dead,
	}
}

// readEstimate is what forgetting ids frees in repo; nil, logged, when restic cannot say.
func readEstimate(ctx context.Context, inspector Inspector, repo string, ids []string) *int64 {
	freed, err := inspector.EstimateForget(ctx, repo, ids)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("repository", repo).Msg("estimating what forgetting dead snapshots frees")
		return nil
	}
	return freed
}
