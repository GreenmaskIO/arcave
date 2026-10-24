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

// captureStatus is what a snapshot's savepoint is.
type captureStatus int

const (
	statusDead captureStatus = iota
	statusCommitted
	statusInProgress
	statusUnknown
)

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
		return model.ResticRepositoryStat{GarbageFindings: gc.NewFindings()}, nil
	}
	inspector, err := s.opener.Open(ctx, scope.Store)
	if err != nil {
		return nil, fmt.Errorf("open the restic repositories: %w", err)
	}
	defer inspector.Close(ctx)

	var (
		judge                    = newSnapshotJudge(scope)
		ops                      = []model.GarbageOp{}
		skipped, unjudged        []model.GarbageSkip
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
		verdict := judge.judgeSnapshots(ctx, key, snapshots)
		ops = append(ops, verdict.ops...)
		skipped = append(skipped, verdict.skipped...)
		unjudged = append(unjudged, verdict.unjudged...)
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
			Skipped:  skipped,
			Unjudged: unjudged,
		},
		LiveRepos:     liveRepos,
		LiveSnapshots: liveSnapshots,
		Unattributed:  unattributed,
		Estimates:     estimates,
	}, nil
}

// snapshotJudge sorts snapshots by the scope's savepoints.
type snapshotJudge struct {
	scope      gc.ComposeProjectScope
	committed  map[model.SavepointID]bool
	inProgress map[model.SavepointID]bool
	dead       map[model.SavepointID]bool
	unknown    map[model.SavepointID]string
}

func newSnapshotJudge(scope gc.ComposeProjectScope) snapshotJudge {
	judge := snapshotJudge{
		scope:      scope,
		committed:  map[model.SavepointID]bool{},
		inProgress: map[model.SavepointID]bool{},
		dead:       map[model.SavepointID]bool{},
		unknown:    map[model.SavepointID]string{},
	}
	for _, savepoint := range scope.Savepoints {
		judge.committed[savepoint.ID] = true
	}
	for _, id := range scope.InProgress {
		judge.inProgress[id] = true
	}
	for _, id := range scope.Dead {
		judge.dead[id] = true
	}
	for _, capture := range scope.Unknown {
		judge.unknown[capture.ID] = capture.Error
	}
	return judge
}

// repoVerdict is what judging one repository's snapshots found; dead are the ids of ops.
type repoVerdict struct {
	live, unattributed int
	ops                []model.GarbageOp
	skipped, unjudged  []model.GarbageSkip
	dead               []string
}

// judgeSnapshots judges every snapshot of the repository at key by its savepoint.
func (j snapshotJudge) judgeSnapshots(ctx context.Context, key string, snapshots []cdc.Snapshot) repoVerdict {
	var (
		live, unattributed int
		ops                []model.GarbageOp
		skipped, unjudged  []model.GarbageSkip
		dead               []string
	)
	for _, snapshot := range snapshots {
		if snapshot.Savepoint == "" {
			unattributed++
			continue
		}
		target := path.Join(key, "snapshots", snapshot.ID)
		switch status, statusErr := j.readStatus(ctx, snapshot.Savepoint); status {
		case statusCommitted:
			live++
		case statusInProgress:
			skipped = append(skipped, model.GarbageSkip{
				Target:       target,
				Reason:       model.GarbageReasonCaptureInProgress,
				LastModified: snapshot.Time,
			})
		case statusUnknown:
			unjudged = append(unjudged, model.GarbageSkip{
				Target:       target,
				Reason:       model.GarbageReasonStatusUnknown,
				LastModified: snapshot.Time,
				Error:        statusErr,
			})
		default:
			dead = append(dead, snapshot.ID)
			ops = append(ops, model.GarbageOp{
				Action:       model.GarbageActionForget,
				Target:       target,
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
		skipped:      skipped,
		unjudged:     unjudged,
		dead:         dead,
	}
}

// readStatus is id's status in the scope; one the scope's listing did not hold is read now, since
// its capture may have started since.
func (j snapshotJudge) readStatus(ctx context.Context, id model.SavepointID) (captureStatus, string) {
	switch {
	case j.committed[id]:
		return statusCommitted, ""
	case j.inProgress[id]:
		return statusInProgress, ""
	case j.dead[id]:
		return statusDead, ""
	}
	if reason, unknown := j.unknown[id]; unknown {
		return statusUnknown, reason
	}
	alive, err := j.scope.ReadCaptureStatus(ctx, id)
	switch {
	case err != nil:
		return statusUnknown, err.Error()
	case alive:
		return statusInProgress, ""
	default:
		return statusDead, ""
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
