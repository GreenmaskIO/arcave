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
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

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

	plan, err := s.planCapture(ctx)
	if err != nil {
		return model.Savepoint{}, preferInterrupt(ctx, err)
	}

	return s.captureHeld(ctx, grantedLock, plan, provider.CaptureRequest{
		ID:          model.NewSavepointID(),
		Name:        req.Name,
		Description: req.Description,
		Labels:      req.Labels,
		InResult:    true,
	}, req.WaitTimeout)
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

// planCapture inspects the workload and plans a savepoint's capture of it, before anything is touched.
func (s *Service) planCapture(ctx context.Context) (provider.CapturePlan, error) {
	live, err := s.workload.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	return live.PlanCapture(ctx, provider.CaptureOptions{})
}

// captureHeld holds the project, captures it and releases it on every path, a heartbeat in the
// savepoint's tree framing the hold and the capture; the hold's or the capture's error wins, a
// release failing under it is only logged. On error the record is zero.
func (s *Service) captureHeld(
	ctx context.Context,
	grantedLock model.GrantedSessionLock,
	plan provider.CapturePlan,
	req provider.CaptureRequest,
	waitTimeout time.Duration,
) (model.Savepoint, error) {
	var (
		log                             = zerolog.Ctx(ctx)
		beatCtx                         context.Context
		beat                            savepointHeartbeat
		held                            provider.HeldWorkload
		savepoint                       model.Savepoint
		err                             error
		beforeErr, captureErr, afterErr error
	)
	if beatCtx, beat, err = s.startSavepointHeartbeat(ctx, req.ID); err != nil {
		return model.Savepoint{}, fmt.Errorf("capturing the savepoint: %w", preferInterrupt(ctx, err))
	}
	if held, beforeErr = plan.Hold(beatCtx); beforeErr != nil {
		// A hold that failed or was interrupted partway may have held some services.
		beforeErr = newCaptureRetainedError(req.ID, preferInterrupt(ctx, beat.fail(ctx, beatCtx, beforeErr)))
	} else if savepoint, captureErr = captureAndCommit(ctx, beatCtx, beat, grantedLock, held, req); captureErr != nil {
		captureErr = newCaptureRetainedError(req.ID, preferInterrupt(ctx, captureErr))
	} else if ctx.Err() != nil {
		log.Info().Msg("savepoint committed before the interrupt: resuming the services")
	}
	afterErr = releaseHeld(ctx, plan, waitTimeout)

	err = beforeErr
	if captureErr != nil {
		err = fmt.Errorf("capturing the savepoint: %w", captureErr)
	}
	switch {
	case err != nil && afterErr != nil:
		return model.Savepoint{}, fmt.Errorf("%w; the services were not resumed: %w", err, afterErr)
	case err != nil:
		return model.Savepoint{}, err
	case afterErr != nil:
		return model.Savepoint{}, fmt.Errorf("savepoint %s committed; bringing the project back: %w", savepoint.ID, afterErr)
	}
	return savepoint, nil
}

// captureAndCommit captures what held holds on beatCtx and commits it while beat is alive and the
// session's lock still this workflow's. On error the record is zero and beat failed, its tree the
// caller's to name.
func captureAndCommit(
	ctx, beatCtx context.Context,
	beat savepointHeartbeat,
	grantedLock model.GrantedSessionLock,
	held provider.HeldWorkload,
	req provider.CaptureRequest,
) (model.Savepoint, error) {
	captured, err := held.Capture(beatCtx, req)
	if err != nil {
		return model.Savepoint{}, beat.fail(ctx, beatCtx, err)
	}
	var savepoint model.Savepoint
	commit := func(ctx context.Context) error {
		var err error
		savepoint, err = captured.Commit(ctx)
		return err
	}
	if err := beat.run.Commit(ctx, grantedLock.Verify, commit); err != nil {
		return model.Savepoint{}, beat.fail(ctx, beatCtx, fmt.Errorf("commit savepoint %s: %w", req.ID, err))
	}
	return savepoint, nil
}
