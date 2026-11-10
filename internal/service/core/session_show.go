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

// ShowSession reports the committed savepoints of the session req.Session names, by compose
// project, newest first, and counts them all; req.Ref marks the current session as ListSessions
// does, and naming no session refuses it.
func (s *SessionService) ShowSession(ctx context.Context, req model.ShowSessionRequest) (model.SessionDetail, error) {
	var (
		listed          = &listedSessions{lister: s.sessions}
		session         model.Session
		marked          model.Session
		current         model.SessionID
		sessions        []model.Session
		groups          []model.ComposeProjectSavepoints
		composeProjects []model.ComposeProjectSavepoints
		err             error
	)
	if session, err = sessionref.Resolve(ctx, listed, s.current, req.Session); err != nil {
		return model.SessionDetail{}, err
	}
	if session.From != nil || (req.Session != req.Ref && req.Ref != "") {
		if sessions, err = listed.ListSessions(ctx); err != nil {
			return model.SessionDetail{}, fmt.Errorf("listing sessions: %w", err)
		}
	}
	switch {
	case req.Session == req.Ref:
		// Resolved by the very ref that marks the current session, it is the current one.
		current = session.ID
	case req.Ref != "":
		// A --session naming no session fails the command, as it fails every other.
		if marked, err = model.FindSessionByRef(sessions, req.Ref); err != nil {
			return model.SessionDetail{}, err
		}
		current = marked.ID
	default:
		current = s.findCurrent(ctx, nil, "")
	}
	if groups, err = s.sessionSavepoints(ctx, session.ID); err != nil {
		return model.SessionDetail{}, err
	}
	composeProjects = newShownComposeProjects(groups, req.IncludeSafetySnapshots)
	savepointCount, safetyCount := countSavepoints(groups)
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Int("compose_projects", len(composeProjects)).
		Msg("session read")
	return model.SessionDetail{
		Session:             session,
		Current:             session.ID == current,
		From:                newSessionOriginRef(session.From, sessions),
		SavepointCount:      savepointCount,
		SafetySnapshotCount: safetyCount,
		ComposeProjects:     composeProjects,
		CheckedAt:           time.Now().UTC(),
	}, nil
}

// listedSessions lists the sessions once however often it is asked, so a resolve and what follows
// it read one listing.
type listedSessions struct {
	lister sessionref.SessionLister
	listed []model.Session
	done   bool
}

func (l *listedSessions) ListSessions(ctx context.Context) ([]model.Session, error) {
	if l.done {
		return l.listed, nil
	}
	listed, err := l.lister.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	l.listed, l.done = listed, true
	return listed, nil
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

// newShownComposeProjects is each group with its savepoints newest first, safety snapshots left out
// unless includeSafety, and a group left with none dropped.
func newShownComposeProjects(
	groups []model.ComposeProjectSavepoints, includeSafety bool,
) []model.ComposeProjectSavepoints {
	shown := make([]model.ComposeProjectSavepoints, 0, len(groups))
	for _, group := range groups {
		var savepoints []model.Savepoint
		if includeSafety {
			savepoints = slices.Clone(group.Savepoints)
		} else {
			savepoints = visibleSavepoints(group.Savepoints)
		}
		if len(savepoints) == 0 {
			continue
		}
		slices.Reverse(savepoints)
		shown = append(shown, model.ComposeProjectSavepoints{
			ComposeProject: group.ComposeProject,
			Savepoints:     savepoints,
		})
	}
	return shown
}
