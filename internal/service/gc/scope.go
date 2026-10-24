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

package gc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// readComposeProjects opens the session's store and names the compose projects it holds, sorted.
func (s *GarbageCollector) readComposeProjects(
	ctx context.Context,
) (storages.Navigator, []model.DockerComposeProjectName, error) {
	sessionStore, err := storefactory.Session(s.store, s.session)
	if err != nil {
		return nil, nil, fmt.Errorf("open session %s: %w", s.session, err)
	}
	names, err := storefactory.ComposeProjects(ctx, sessionStore)
	if err != nil {
		return nil, nil, fmt.Errorf("list compose projects of session %s: %w", s.session, err)
	}
	return sessionStore, names, nil
}

// readComposeProjectScope opens one compose project's store, lists it once, reads the records
// that mark it off that listing, and the heartbeat of every tree no record commits.
func (s *GarbageCollector) readComposeProjectScope(
	ctx context.Context, sessionStore storages.Navigator, name model.DockerComposeProjectName,
) (ComposeProjectScope, error) {
	var (
		store           storages.Storager
		objects         []storages.ObjectStat
		trees           []model.SavepointID
		savepoints      []model.Savepoint
		captures        captureStatuses
		savepointReader SavepointReader
		err             error
	)
	if store, err = storefactory.ComposeProject(sessionStore, name); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("open compose project %s: %w", name, err)
	}
	if objects, err = s.listComposeProject(ctx, store); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("list compose project %s: %w", name, err)
	}
	savepointReader = s.newSavepoints(store)
	trees = newSavepointTrees(objects)
	if savepoints, err = savepointReader.LoadSavepoints(ctx, trees); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("read savepoints of compose project %s: %w", name, err)
	}
	if captures, err = s.readCaptureStatuses(ctx, store, savepointReader, newUncommittedTrees(savepoints, trees)); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("read the captures of compose project %s: %w", name, err)
	}
	return ComposeProjectScope{
		ComposeProject: name,
		Store:          store,
		Objects:        objects,
		Savepoints:     savepoints,
		InProgress:     captures.inProgress,
		Dead:           captures.dead,
		Unknown:        captures.unknown,
		ReadCaptureStatus: func(ctx context.Context, id model.SavepointID) (bool, error) {
			status := s.readCaptureStatus(ctx, store, savepointReader, id)
			return status.alive, status.err
		},
	}, nil
}

// listComposeProject lists store recursively, sorted, leaving out its top-level directories of
// s.unlisted.
func (s *GarbageCollector) listComposeProject(ctx context.Context, store storages.Storager) ([]storages.ObjectStat, error) {
	files, dirs, err := store.ListDir(ctx)
	if err != nil {
		return nil, err
	}
	objects := []storages.ObjectStat{}
	for _, file := range files {
		stat, err := store.Stat(file)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", file, err)
		}
		if !stat.Exist {
			continue
		}
		stat.Name = file
		objects = append(objects, *stat)
	}
	for _, dir := range dirs {
		name := dir.Dirname()
		if slices.Contains(s.unlisted, name) {
			continue
		}
		members, err := dir.List(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", name, err)
		}
		for _, member := range members {
			member.Name = path.Join(name, member.Name)
			objects = append(objects, member)
		}
	}
	slices.SortFunc(objects, func(a, b storages.ObjectStat) int { return strings.Compare(a.Name, b.Name) })
	return objects, nil
}

// captureStatuses are the uncommitted trees of a scope by what their heartbeat says.
type captureStatuses struct {
	inProgress []model.SavepointID
	dead       []model.SavepointID
	unknown    []UnknownCapture
}

// readCaptureStatuses sorts every uncommitted savepoint tree of ids by its heartbeat, read off
// the store rather than the listing, at most s.concurrency at once.
func (s *GarbageCollector) readCaptureStatuses(
	ctx context.Context, store storages.Navigator, savepoints SavepointReader, ids []model.SavepointID,
) (captureStatuses, error) {
	var (
		statuses = make([]captureStatus, len(ids))
		captures captureStatuses
		wg       sync.WaitGroup
		slots    = make(chan struct{}, s.concurrency)
	)
	for i, id := range ids {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			statuses[i] = s.readCaptureStatus(ctx, store, savepoints, id)
		})
	}
	wg.Wait()
	// A read cancelled midway reports unknown: the statuses of a cancelled run are no verdict.
	if err := context.Cause(ctx); err != nil {
		return captureStatuses{}, err
	}
	for i, id := range ids {
		switch status := statuses[i]; {
		case status.err != nil:
			captures.unknown = append(captures.unknown, UnknownCapture{
				ID:    id,
				Error: status.err.Error(),
			})
		case status.alive:
			captures.inProgress = append(captures.inProgress, id)
		default:
			captures.dead = append(captures.dead, id)
		}
	}
	return captures, nil
}

// captureStatus is what one tree's heartbeat says: alive, dead, or err when it cannot say.
type captureStatus struct {
	alive bool
	err   error
}

// newSavepointTrees lists, sorted, the savepoint trees objects holds.
func newSavepointTrees(objects []storages.ObjectStat) []model.SavepointID {
	trees := map[model.SavepointID]bool{}
	for _, object := range objects {
		// <savepoint-id>/…: a directory the gate refuses is not a savepoint's.
		dir, _, _ := strings.Cut(object.Name, "/")
		if id := model.SavepointID(dir); id.Validate() == nil {
			trees[id] = true
		}
	}
	return slices.Sorted(maps.Keys(trees))
}

// newUncommittedTrees lists the trees of trees no record of savepoints commits.
func newUncommittedTrees(savepoints []model.Savepoint, trees []model.SavepointID) []model.SavepointID {
	return slices.DeleteFunc(slices.Clone(trees), func(id model.SavepointID) bool {
		return slices.ContainsFunc(savepoints, func(savepoint model.Savepoint) bool { return savepoint.ID == id })
	})
}

// readCaptureStatus reads id's heartbeat. None is dead: a capture writes it before its first
// object. A fresh in-progress one is alive; any other is dead unless a record stands now, since
// a capture commits before it marks its heartbeat done and may have committed since the listing.
func (s *GarbageCollector) readCaptureStatus(
	ctx context.Context, store storages.Navigator, savepoints SavepointReader, id model.SavepointID,
) captureStatus {
	tree, err := store.SubStorage(id.String(), true)
	if err != nil {
		return captureStatus{err: fmt.Errorf("open tree %s: %w", id, err)}
	}
	beat, err := heartbeat.NewReader(tree).SetStaleAfter(s.staleAfter).Read(ctx)
	switch {
	case errors.Is(err, model.ErrHeartbeatNotFound):
		return captureStatus{}
	case err != nil:
		zerolog.Ctx(ctx).Warn().Err(err).Str("savepoint", id.String()).Msg("reading the capture heartbeat")
		return captureStatus{err: err}
	case beat.Operation != model.HeartbeatOperationSavepoint:
		return captureStatus{err: fmt.Errorf("heartbeat of operation %q, not a capture's", beat.Operation)}
	case beat.Status == heartbeat.StatusInProgress:
		return captureStatus{alive: true}
	}
	committed, err := savepoints.IsSavepointCommitted(ctx, id)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("savepoint", id.String()).Msg("reading the savepoint record")
		return captureStatus{err: err}
	}
	return captureStatus{alive: committed}
}
