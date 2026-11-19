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

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/core/internal/workflow"
)

// rollbackRecovery is what a rollback takes over from the interrupted one it recovers from; zero
// when it recovers from none.
type rollbackRecovery struct {
	recovering bool
	// safety is the safety snapshot the standing marker names: the recovery's marker keeps naming
	// it, as the last copy of the state before them both.
	safety model.SavepointID
}

// locked runs a workflow with the project lock and the session's lock held, once its session is
// known to still exist: one deleted since the command resolved it must not be written into again.
// fn gets the session lock, its context and the session as its record names it.
func locked[T any](
	ctx context.Context, s *SavepointStoreService, operation model.SessionLockOperation,
	fn func(ctx context.Context, grantedLock model.GrantedSessionLock, session model.SessionRef) (T, error),
) (T, error) {
	return workflow.WithWriteUnlock(ctx, s.projectLocker, s.writeAdmission, func() (T, error) {
		return workflow.WithSessionLock(ctx, s.sessionLocker, operation, func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error) {
			session, err := workflow.ReadSessionRef(ctx, s.sessions, s.session)
			if err != nil {
				var zero T
				return zero, err
			}
			return fn(ctx, grantedLock, session)
		})
	})
}

// requireNoInflight refuses to run while .arcave/inflight.json stands, except for the rollback
// that recovers from it. restoring is the savepoint the caller rolls back to, "" for any other
// workflow. It reports what the caller, if it is that recovery, takes over.
func (s *SavepointStoreService) requireNoInflight(ctx context.Context, restoring model.SavepointID) (rollbackRecovery, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return rollbackRecovery{}, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return rollbackRecovery{}, nil
	}
	if s.isRecoveryRollback(marker, restoring) {
		zerolog.Ctx(ctx).Info().Str("interrupted_target", marker.Target.String()).
			Msg("recovering from an interrupted rollback; the marker clears on success")
		return rollbackRecovery{
			recovering: true,
			safety:     marker.Safety,
		}, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("interrupted_target", marker.Target.String()).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Str("safety_snapshot_id", marker.Safety.String()).
		Str("phase", string(marker.Phase)).
		Msg(workflow.RecoveryHint(marker))
	return rollbackRecovery{}, err
}

// isRecoveryRollback reports whether a rollback to id, in this session and compose project,
// recovers from the interrupted rollback the marker records: one to its safety snapshot or, when
// the marker accepts any, one to any savepoint.
func (s *SavepointStoreService) isRecoveryRollback(marker model.InflightMarker, id model.SavepointID) bool {
	if id == "" || marker.Session != s.session || marker.ComposeProject != s.composeProject {
		return false
	}
	return marker.AcceptsAnySavepoint() || id == marker.Safety
}
