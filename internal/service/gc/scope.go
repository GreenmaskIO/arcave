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
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
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

// readComposeProjectScope opens one compose project's store, lists it once and reads the records
// that mark it off that listing; every tree none commits is dead.
func (s *GarbageCollector) readComposeProjectScope(
	ctx context.Context, sessionStore storages.Navigator, name model.DockerComposeProjectName,
) (ComposeProjectScope, error) {
	var (
		store      storages.Storager
		objects    []storages.ObjectStat
		trees      []model.SavepointID
		savepoints []model.Savepoint
		err        error
	)
	if store, err = storefactory.ComposeProject(sessionStore, name); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("open compose project %s: %w", name, err)
	}
	if objects, err = s.listComposeProject(ctx, store); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("list compose project %s: %w", name, err)
	}
	trees = newSavepointTrees(objects)
	if savepoints, err = s.newSavepoints(store).LoadSavepoints(ctx, trees); err != nil {
		return ComposeProjectScope{}, fmt.Errorf("read savepoints of compose project %s: %w", name, err)
	}
	return ComposeProjectScope{
		ComposeProject: name,
		Store:          store,
		Objects:        objects,
		Savepoints:     savepoints,
		Dead:           newUncommittedTrees(savepoints, trees),
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
