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
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/core/internal/workflow"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// clearedSafetySnapshotNotice says what a forced down leaves of the rollback whose marker it
// cleared: the safety snapshot is the last copy of the state before it, and nothing guards it now.
const clearedSafetySnapshotNotice = "the inflight marker of an interrupted rollback was cleared; " +
	"its safety snapshot %s is the last copy of the state before that rollback, and a gc may now retire it"

// DownDeps are DownService's collaborators: a down reads the daemon and the inflight marker, and
// writes nothing to the store, so it takes neither a session lock nor a write admission.
type DownDeps struct {
	ProjectLocker projectLocker
	Inflight      inflightMarkers
	// Sessions names Session in the result.
	Sessions sessionLookup
	// Session is the session the down runs in; zero means model.DefaultSession.
	Session        model.SessionID
	ArcaveProject  model.ArcaveProjectName
	ComposeProject model.DockerComposeProjectName
	// Workload is the compose project the down takes off the daemon.
	Workload provider.Surveyor
}

// DownService ends a compose project's environment on the daemon.
type DownService struct {
	projectLocker  projectLocker
	inflightMarker inflightMarkers
	sessions       sessionLookup
	session        model.SessionID
	arcaveProject  model.ArcaveProjectName
	composeProject model.DockerComposeProjectName
	workload       provider.Surveyor
}

func NewDownService(deps DownDeps) *DownService {
	return &DownService{
		projectLocker:  deps.ProjectLocker,
		inflightMarker: deps.Inflight,
		sessions:       deps.Sessions,
		session:        cmp.Or(deps.Session, model.DefaultSession),
		arcaveProject:  deps.ArcaveProject,
		composeProject: deps.ComposeProject,
		workload:       deps.Workload,
	}
}

// Down takes off the daemon everything of the compose project — containers, networks, the volumes
// it owns, its containers' anonymous volumes and its restored layer tags — once every replica's
// deployment labels name this session of this arcave project. A bind and an external volume stay.
// What changed since the last savepoint is lost: a down takes none. req.Force skips the check.
func (s *DownService) Down(ctx context.Context, req model.DownRequest) (_ model.DownResult, err error) {
	ctx, step := progress.Begin(ctx, progress.Step{Kind: kindDown})
	defer func() { step.End(err) }()

	// The flock alone: the down writes nothing to the store, and the daemon is host-local, as the
	// lock and the marker are.
	return workflow.WithUnlock(ctx, s.projectLocker, func() (model.DownResult, error) {
		return s.down(ctx, req)
	})
}

// down is the workflow itself, run with the project lock held.
func (s *DownService) down(ctx context.Context, req model.DownRequest) (model.DownResult, error) {
	marker, err := s.requireDownMarker(ctx, req.Force)
	if err != nil {
		return model.DownResult{}, err
	}
	session, err := workflow.ReadSessionRef(ctx, s.sessions, s.session)
	if err != nil {
		return model.DownResult{}, err
	}
	surveyed, err := s.workload.Survey(ctx)
	if err != nil {
		return model.DownResult{}, preferInterrupt(ctx, err)
	}
	deployment, err := surveyed.Owner(s.arcaveProject, session.ID)
	if err != nil && !req.Force {
		return model.DownResult{}, err
	}

	// Proved gone, not taken on compose's word: what is left keeps the marker and the tags.
	dropped, err := surveyed.Drop(ctx)
	if err != nil {
		return model.DownResult{}, preferInterrupt(ctx, err)
	}

	// On a revertContext: an interrupt past the drop must not leave the marker of an environment
	// that is gone.
	cleanupCtx, cancel := revertContext(ctx)
	defer cancel()
	markerNotices, err := s.clearMarker(cleanupCtx, marker)
	if err != nil {
		return model.DownResult{}, err
	}
	networkNotices := dropped.Notices()
	for _, notice := range networkNotices {
		progress.Current(cleanupCtx).WarnInResult(notice.Message)
	}
	removedTags, tagNotices := dropped.RemoveRestoredLayers(cleanupCtx)
	return model.DownResult{
		ComposeProject:  s.composeProject,
		Session:         session,
		Deployment:      deployment,
		Forced:          req.Force,
		ClearedInflight: marker,
		Discarded:       surveyed.Resources(),
		RemovedTags:     removedTags,
		Notices:         slices.Concat(networkNotices, markerNotices, tagNotices),
	}, nil
}

// requireDownMarker refuses a down while an inflight marker stands: another compose project's
// always, as it guards that one's environment, and this one's — from any session — unless force,
// which returns it for the down to clear once the environment it guards is gone.
func (s *DownService) requireDownMarker(ctx context.Context, force bool) (*model.InflightMarker, error) {
	marker, found, err := s.inflightMarker.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if !found {
		return nil, nil
	}
	if force && marker.ComposeProject == s.composeProject {
		return &marker, nil
	}
	err = &model.InterruptedRollbackError{Marker: marker}
	zerolog.Ctx(ctx).Error().Err(err).
		Str("session", marker.Session.String()).
		Str("compose_project", marker.ComposeProject.String()).
		Msg(workflow.RecoveryHint(marker))
	return nil, err
}

// clearMarker clears the marker a forced down took over, nil for none, and says what is left of
// the rollback it recorded.
func (s *DownService) clearMarker(ctx context.Context, marker *model.InflightMarker) ([]model.Notice, error) {
	if marker == nil {
		return nil, nil
	}
	if err := s.inflightMarker.Clear(ctx); err != nil {
		return nil, fmt.Errorf("the environment is down; clearing the inflight marker: %w", err)
	}
	if marker.Safety == "" {
		return nil, nil
	}
	notice := fmt.Sprintf(clearedSafetySnapshotNotice, marker.Safety)
	progress.Current(ctx).WarnInResult(notice)
	return []model.Notice{{Message: notice, Severe: true}}, nil
}
