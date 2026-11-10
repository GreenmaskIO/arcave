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
	"slices"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/sessionref"
)

// ShowSession reports the savepoints of the session req.Session names, by compose project, newest
// first, marking it current as ListSessions does; shown as the current session, it must resolve.
func (s *SessionService) ShowSession(ctx context.Context, req model.ShowSessionRequest) (model.SessionDetail, error) {
	var (
		sessions        []model.Session
		session         model.Session
		source          model.CurrentSessionSource
		current         model.SessionID
		currentSource   *model.CurrentSessionSource
		groups          []model.ComposeProjectSavepoints
		composeProjects []model.ComposeProjectSavepoints
		err             error
	)
	if sessions, err = s.sessions.ListSessions(ctx); err != nil {
		return model.SessionDetail{}, fmt.Errorf("listing sessions: %w", err)
	}
	switch req.Session {
	case "", model.RefCurrent:
		if session, source, err = sessionref.ResolveCurrent(ctx, sessionref.Listed(sessions), s.current,
			req.Current); err != nil {
			return model.SessionDetail{}, err
		}
		currentSource = &source
	default:
		if session, err = findNamedSession(sessions, req.Session); err != nil {
			return model.SessionDetail{}, err
		}
		if current, currentSource, err = s.findCurrent(ctx, sessions, req.Current); err != nil {
			return model.SessionDetail{}, err
		}
		if session.ID != current {
			currentSource = nil
		}
	}
	if groups, err = s.sessionSavepoints(ctx, session.ID); err != nil {
		return model.SessionDetail{}, err
	}
	composeProjects = newShownComposeProjects(groups)
	savepointCount, safetyCount := countSavepoints(groups)
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Int("compose_projects", len(composeProjects)).
		Msg("session read")
	return model.SessionDetail{
		Session:             session,
		CurrentSource:       currentSource,
		From:                newSessionOriginRef(session.From, sessions),
		SavepointCount:      savepointCount,
		SafetySnapshotCount: safetyCount,
		ComposeProjects:     composeProjects,
		CheckedAt:           time.Now().UTC(),
	}, nil
}

// findNamedSession is the session ref names among sessions: RefLatest the newest, any other by name
// or id.
func findNamedSession(sessions []model.Session, ref string) (model.Session, error) {
	if ref != model.RefLatest {
		return model.FindSessionByRef(sessions, ref)
	}
	session, _ := model.FindLatestSession(sessions)
	return session, nil
}

// newSessionOriginRef names the session origin points at, by the name sessions list it under; nil for
// no origin, no name for one since removed.
func newSessionOriginRef(origin *model.SessionOrigin, sessions []model.Session) *model.SessionRef {
	if origin == nil {
		return nil
	}
	source, _ := model.FindSessionByID(sessions, origin.Session.String())
	return &model.SessionRef{
		ID:   origin.Session,
		Name: source.Name,
	}
}

// newShownComposeProjects is each group with its savepoints newest first, safety snapshots among them.
func newShownComposeProjects(groups []model.ComposeProjectSavepoints) []model.ComposeProjectSavepoints {
	shown := make([]model.ComposeProjectSavepoints, 0, len(groups))
	for _, group := range groups {
		savepoints := slices.Clone(group.Savepoints)
		slices.Reverse(savepoints)
		shown = append(shown, model.ComposeProjectSavepoints{
			ComposeProject: group.ComposeProject,
			Savepoints:     savepoints,
		})
	}
	return shown
}
