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

	"github.com/greenmaskio/arcave/internal/model"
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
	if err := req.Selector().Validate(); err != nil {
		return model.UpResult{}, err
	}
	// Every container it builds is labelled with the arcave project, which status and down read back.
	if err := s.arcaveProject.Validate(); err != nil {
		return model.UpResult{}, fmt.Errorf("arcave project of the deployment labels: %w", err)
	}

	return locked(ctx, s.SavepointStoreService, model.SessionLockOperationUp,
		func(ctx context.Context, _ model.GrantedSessionLock, session model.SessionRef) (model.UpResult, error) {
			return s.up(ctx, session, req)
		},
	)
}

// up is the workflow itself, run with the project lock and the session lock held.
func (s *Service) up(ctx context.Context, session model.SessionRef, req model.UpRequest) (model.UpResult, error) {
	selector := req.Selector()
	savepoint, err := s.resolveSavepoint(ctx, selector)
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
	if err := requireDaemon(ctx, s.daemonVersion, s.minDaemonMajor); err != nil {
		return model.UpResult{}, err
	}
	// The narrow read, not the introspection: a broken environment it would trip on is what a
	// forced up is for.
	resources, err := readResources(ctx, s.composeInspector)
	if err != nil {
		logInterrupt(ctx, "up interrupted before anything was created: nothing changed")
		return model.UpResult{}, preferInterrupt(ctx, err)
	}
	// Every volume of the compose project counts, not only those the savepoint restores: compose
	// would reuse one the record does not hold, its old data mixed in. A network holds no data.
	present := len(resources.Containers) > 0 || len(resources.Volumes) > 0
	if present && !req.Force {
		return model.UpResult{}, &model.ComposeProjectPresentError{
			Containers: resources.Containers,
			Volumes:    resources.Volumes,
		}
	}

	owned := make(map[string]bool, len(resources.Volumes))
	for _, name := range resources.Volumes {
		owned[name] = true
	}
	var (
		drop      *model.ComposeProjectResources
		discarded *model.DiscardedResources
	)
	if present {
		drop = &resources
		dropped := model.NewDiscardedResources(resources)
		discarded = &dropped
	}
	built, err := s.rollbackColdStart(ctx, selector, session, savepoint, owned, rollbackRecovery{}, drop)
	if err != nil {
		return model.UpResult{}, err
	}
	return model.UpResult{
		RolledBackTo:   built.RolledBackTo,
		Restored:       built.Restored,
		WritableLayers: built.WritableLayers,
		Discarded:      discarded,
	}, nil
}
