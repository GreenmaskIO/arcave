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

package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/greenmaskio/arcave/internal/model"
)

// errSavepointNotFound is returned when no savepoint matches the requested id or name.
var errSavepointNotFound = errors.New("savepoint not found")

// resolveSavepoint resolves req's selector to the record to roll back to.
// Resolution stays in memory: the store has no index to query.
//
// It reads every record, safety snapshots included, and findSavepoint decides
// which of them this selector may reach.
func (s *Service) resolveSavepoint(ctx context.Context, req model.RollbackRequest) (model.Savepoint, error) {
	savepoints, err := s.listSavepoints(ctx, true)
	if err != nil {
		return model.Savepoint{}, err
	}
	savepoint, err := findSavepoint(savepoints, req)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("finding savepoint %q: %w", req.Ref(), err)
	}
	return savepoint, nil
}

// findSavepoint resolves a request's selector to one record. Ids win, being
// unique; a name matching several is an error, never a silent guess.
//
// It takes every record so it can apply the visibility rule: an id resolves
// against all of them — rolling back to a rollback's undo is spelled by id — while
// Name and Latest see only what `savepoint list` shows.
//
// The caller has run req.Validate, so the branches read nil-ness alone.
func findSavepoint(savepoints []model.Savepoint, req model.RollbackRequest) (model.Savepoint, error) {
	if req.ID != nil {
		for _, savepoint := range savepoints {
			if savepoint.ID == *req.ID {
				return savepoint, nil
			}
		}
		return model.Savepoint{}, fmt.Errorf("%w: %s", errSavepointNotFound, *req.ID)
	}

	savepoints = visibleSavepoints(savepoints)
	if len(savepoints) == 0 {
		return model.Savepoint{}, model.ErrNoSavepoints
	}

	if req.Name == nil {
		if req.Latest == nil || !*req.Latest {
			return model.Savepoint{}, model.ErrRollbackNoSelector
		}
		latest := savepoints[0]
		for _, savepoint := range savepoints[1:] {
			if savepoint.CreatedAt.After(latest.CreatedAt) {
				latest = savepoint
			}
		}
		return latest, nil
	}

	byName := findSavepointsByName(savepoints, *req.Name)
	switch len(byName) {
	case 0:
		return model.Savepoint{}, fmt.Errorf("%w: %s", errSavepointNotFound, *req.Name)
	case 1:
		return byName[0], nil
	default:
		return model.Savepoint{}, newAmbiguousSavepointNameError(*req.Name, byName)
	}
}

// findSavepointsByName returns every record carrying name.
func findSavepointsByName(savepoints []model.Savepoint, name string) []model.Savepoint {
	var byName []model.Savepoint
	for _, savepoint := range savepoints {
		if savepoint.Name == name {
			byName = append(byName, savepoint)
		}
	}
	return byName
}

// newAmbiguousSavepointNameError names the savepoints carrying name, ids sorted.
func newAmbiguousSavepointNameError(name string, savepoints []model.Savepoint) error {
	ids := make([]model.SavepointID, len(savepoints))
	for i, savepoint := range savepoints {
		ids[i] = savepoint.ID
	}
	slices.Sort(ids)
	return &model.AmbiguousSavepointNameError{
		Name: name,
		IDs:  ids,
	}
}

// visibleSavepoints drops the safety snapshots. Non-nil, so an empty listing
// marshals as [] rather than null.
func visibleSavepoints(savepoints []model.Savepoint) []model.Savepoint {
	visible := make([]model.Savepoint, 0, len(savepoints))
	for _, savepoint := range savepoints {
		if !savepoint.IsSafetySnapshot() {
			visible = append(visible, savepoint)
		}
	}
	return visible
}
