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

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/core/internal/workflow"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// Up builds the compose project from a savepoint, as a rollback into a compose project with no
// containers does, and never over anything: what the daemon still holds of it, containers or
// volumes it owns, refuses the up before anything is written, unless req.Force drops it first,
// with no backup.
func (s *Service) Up(ctx context.Context, req model.UpRequest) (_ model.UpResult, err error) {
	// Outside the lock: a lost lock cancels the work under it, and the root says why.
	ctx, step := progress.Begin(ctx, progress.Step{
		Kind: kindUp,
		Name: req.Ref(),
	})
	defer func() { step.End(err) }()
	// Ahead of the lock, as a rollback's.
	selector := req.Selector()
	if err := selector.Validate(); err != nil {
		return model.UpResult{}, err
	}
	// Every container it builds is labelled with the arcave project, which status and down read back.
	if err := s.arcaveProject.Validate(); err != nil {
		return model.UpResult{}, fmt.Errorf("arcave project of the deployment labels: %w", err)
	}
	// A savepoint named and not there is refused before the wait on the lock, as a rollback's.
	if req.ID != nil || req.Name != nil {
		if _, err := s.resolveSavepoint(ctx, selector); errors.Is(err, errSavepointNotFound) {
			return model.UpResult{}, err
		}
	}

	return locked(ctx, s.SavepointStoreService, model.SessionLockOperationUp,
		func(ctx context.Context, _ model.GrantedSessionLock, session model.SessionRef) (model.UpResult, error) {
			return s.up(ctx, session, req)
		},
	)
}

// up is the workflow itself, run under the session lock.
func (s *Service) up(ctx context.Context, session model.SessionRef, req model.UpRequest) (model.UpResult, error) {
	savepoint, err := s.resolveSavepoint(ctx, req.Selector())
	if err != nil {
		return model.UpResult{}, err
	}
	if err := savepoint.ID.Validate(); err != nil {
		return model.UpResult{}, fmt.Errorf("validate savepoint id: %w", err)
	}
	// Any marker refuses, forced or not: recovering from it is a rollback's, which its hint names.
	if _, err := s.requireNoInflight(ctx, ""); err != nil {
		return model.UpResult{}, fmt.Errorf("require no inflight: %w", err)
	}
	// The narrow read, not the inspection: a broken environment it would trip on is what a forced
	// up is for.
	surveyed, err := s.workload.Survey(ctx)
	if err != nil {
		workflow.LogInterrupt(ctx, "up interrupted before anything was created: nothing changed")
		return model.UpResult{}, preferInterrupt(ctx, err)
	}
	// Every volume of the compose project counts, not only those the savepoint restores: compose
	// would reuse one the record does not hold, its old data mixed in.
	present := surveyed.Present()
	if present && !req.Force {
		resources := surveyed.Resources()
		return model.UpResult{}, &model.ComposeProjectPresentError{
			Containers: resources.Containers,
			Volumes:    resources.Volumes,
		}
	}

	built, err := s.upOver(ctx, req, session, savepoint, surveyed, present)
	if err != nil {
		return model.UpResult{}, err
	}
	var discarded *model.DiscardedResources
	if present {
		dropped := surveyed.Resources()
		discarded = &dropped
	}
	return model.UpResult{
		RolledBackTo: built.RolledBackTo,
		Restored:     built.Restored,
		Detail:       built.Detail,
		Discarded:    discarded,
	}, nil
}

// upOver builds savepoint over what surveyed found, dropping it first: nothing is stopped or
// snapshotted, so a failure only removes what it created. Over a present compose project the drop
// takes its volumes with no backup; over an absent one it takes what is left that holds no data.
func (s *Service) upOver(
	ctx context.Context,
	req model.UpRequest,
	session model.SessionRef,
	savepoint model.Savepoint,
	surveyed provider.SurveyedWorkload,
	present bool,
) (model.RollbackResult, error) {
	log := zerolog.Ctx(ctx)
	if present {
		log.Info().Msg("dropping what the daemon holds of the compose project: building it from the savepoint")
	} else {
		log.Info().Msg("the compose project is not running: building it from the savepoint")
	}

	prepared, err := s.runPrepare(ctx, surveyed, session, savepoint, false,
		"up interrupted before anything was created: nothing changed")
	if err != nil {
		return model.RollbackResult{}, err
	}
	// An interrupt that landed during the prepare stops here, ahead of the drop: what the daemon
	// holds is untouched, and a marker would refuse the next workflow over it.
	if err := context.Cause(ctx); err != nil {
		workflow.LogInterrupt(ctx, "up interrupted before anything was created: nothing changed")
		return model.RollbackResult{}, fmt.Errorf("up interrupted, nothing changed: %w", err)
	}

	// No safety snapshot of its own: the marker tells the user how to recover.
	if err := s.putInflightMarker(ctx, newFirstMarkerPhase(false), savepoint.ID, ""); err != nil {
		return model.RollbackResult{}, fmt.Errorf("recording the inflight marker, nothing changed: %w", err)
	}

	var vacant provider.VacantWorkload
	restored, err := runStep(ctx, kindRollbackApply, func(ctx context.Context) (provider.RestoredWorkload, error) {
		// Proved gone before the build: compose's down alone leaves a busy volume, which the build
		// would reuse with its old data. A network left holds none, and the build reuses it.
		dropped, err := surveyed.Drop(ctx)
		if err != nil {
			return nil, err
		}
		if vacant, err = dropped.Vacant(prepared); err != nil {
			return nil, err
		}
		return s.buildFromSavepoint(ctx, vacant, prepared, savepoint.ID, "", req.WaitTimeout)
	})
	if err != nil {
		workflow.LogInterrupt(ctx, "up interrupted: removing what it created")
		s.abortColdStart(ctx, vacant)
		return model.RollbackResult{}, fmt.Errorf("up failed: %w", preferInterrupt(ctx, err))
	}

	// On a revertContext, as every path's cleanup; nothing was orphaned, the drop leaving nothing.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	s.cleanUpRollback(cleanupCtx, restored)
	return newRollbackResult(savepoint, prepared.Summary(), "", nil), nil
}
