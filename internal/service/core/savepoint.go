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
	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/rs/zerolog"
)

// errNoVolumesToBackUp refuses a savepoint that would cover nothing. It stays
// unexported: no adapter matches on it, so it is not a seam contract and does
// not belong in model.
var errNoVolumesToBackUp = errors.New("no stateful volumes found; nothing to capture")

// errSavepointNotFound is returned when no savepoint matches the requested id or name.
var errSavepointNotFound = errors.New("savepoint not found")

// errCaptureDirectoryNotEmpty refuses to capture into a directory that already
// holds data. Unexported for the same reason: nothing matches on it.
var errCaptureDirectoryNotEmpty = errors.New("capture directory is not empty")

// CreateSavepoint stops the services, captures every volume under one id, commits
// the record, and starts the services again. The project lock is held throughout.
//
// On error the record is zero, whatever the store holds: a savepoint committed
// before a failed restart is named by the error and found by `savepoint list`.
func (s *Service) CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (_ model.Savepoint, err error) {
	// Outside the lock: a lost lock cancels the work under it, and the root says why.
	ctx, step := progress.Begin(ctx, progress.Step{
		Kind: kindSavepointCreate,
		Name: req.Name,
	})
	defer func() { step.End(err) }()
	return locked(ctx, s.SavepointStoreService, model.SessionLockOperationSavepointCreate,
		func(ctx context.Context, grantedLock model.GrantedSessionLock, _ model.SessionRef) (model.Savepoint, error) {
			return s.createSavepoint(ctx, grantedLock, req)
		})
}

// createSavepoint is the workflow itself, run with the project lock and the session lock held.
func (s *Service) createSavepoint(
	ctx context.Context, grantedLock model.GrantedSessionLock, req model.CreateSavepointRequest,
) (model.Savepoint, error) {
	log := zerolog.Ctx(ctx)

	if err := req.Validate(); err != nil {
		return model.Savepoint{}, err
	}
	if _, err := s.requireNoInflight(ctx, ""); err != nil {
		return model.Savepoint{}, err
	}
	visible, err := s.listSavepoints(ctx, false)
	if err != nil {
		return model.Savepoint{}, err
	}
	if err := requireUniqueName(visible, req.Name); err != nil {
		return model.Savepoint{}, err
	}

	working, err := s.prepare(ctx, req.WaitTimeout)
	if err != nil {
		logInterrupt(ctx, "savepoint interrupted before anything was stopped: nothing changed")
		return model.Savepoint{}, preferInterrupt(ctx, err)
	}

	killed, err := s.stopServices(ctx, working)
	if err != nil {
		// A stop that failed or was interrupted partway may have stopped some services.
		logInterrupt(ctx, "savepoint interrupted while stopping services: restarting them, nothing captured")
		if err := s.restartServices(ctx, working); err != nil {
			log.Error().Err(err).Msg("services failed to restart after stop failure")
		}
		return model.Savepoint{}, preferInterrupt(ctx, err)
	}

	// Capture while the services are stopped. The capture error is what the caller
	// gets; the restart on top of it is best-effort and only logged.
	savepoint, err := s.performSavepoint(ctx, grantedLock, working, killed, req.Name, req.Description, req.Labels)
	if err != nil {
		logInterrupt(ctx, "savepoint interrupted during capture: restarting services, nothing committed")
		err = preferInterrupt(ctx, err)
		if err := s.restartServices(ctx, working); err != nil {
			log.Error().Err(err).Msg("services failed to restart after capture failure")
		}
		return model.Savepoint{}, fmt.Errorf("capturing volumes: %w", err)
	}

	logInterrupt(ctx, "savepoint committed before the interrupt: restarting services")
	if err := s.restartServices(ctx, working); err != nil {
		return model.Savepoint{}, fmt.Errorf("savepoint %s committed; services failed to start: %w", savepoint.ID, err)
	}
	return savepoint, nil
}

// requireUniqueName refuses a name another of the visible savepoints carries. Safety
// snapshots do not count: a name selector never sees them.
func requireUniqueName(visible []model.Savepoint, name string) error {
	if name == "" {
		return nil
	}
	for _, savepoint := range visible {
		if savepoint.Name == name {
			return fmt.Errorf("%w: %q names savepoint %s", model.ErrSavepointNameTaken, name, savepoint.ID)
		}
	}
	return nil
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
		return model.Savepoint{}, errors.New("no savepoints exist")
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

// findSavepointsByID returns the record carrying id, if any.
func findSavepointsByID(savepoints []model.Savepoint, id model.SavepointID) []model.Savepoint {
	for _, savepoint := range savepoints {
		if savepoint.ID == id {
			return []model.Savepoint{savepoint}
		}
	}
	return nil
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
