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
)

type sessionRepository interface {
	SaveSession(ctx context.Context, session model.Session) error
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
	ListSessions(ctx context.Context) ([]model.Session, error)
	DeleteSession(ctx context.Context, id model.SessionID) error
	ListSessionSavepoints(ctx context.Context, id model.SessionID) ([]model.ComposeProjectSavepoints, error)
}

// SessionDeps are SessionService's collaborators.
type SessionDeps struct {
	Sessions sessionRepository
	Locker   projectLocker
	Inflight inflightMarkers
	// Now stamps a new session's record; nil means time.Now.
	Now func() time.Time
}

// SessionService manages an arcave project's sessions. It touches no daemon and names
// no compose project.
type SessionService struct {
	sessions sessionRepository
	locker   projectLocker
	inflight inflightMarkers
	now      func() time.Time
}

func NewSessionService(d SessionDeps) *SessionService {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &SessionService{
		sessions: d.Sessions,
		locker:   d.Locker,
		inflight: d.Inflight,
		now:      now,
	}
}

// ResolveSession finds the session ref names: by name first, then by id; "" is the default.
func (s *SessionService) ResolveSession(ctx context.Context, ref string) (model.Session, error) {
	if ref == "" {
		return model.DefaultSessionRecord, nil
	}
	records, err := s.sessions.ListSessions(ctx)
	if err != nil {
		return model.Session{}, fmt.Errorf("listing sessions: %w", err)
	}
	if record, found, err := sessionByName(records, ref); err != nil || found {
		return record, err
	}
	if record, found := sessionByID(records, ref); found {
		return record, nil
	}
	return model.Session{}, fmt.Errorf("%w: %q", model.ErrSessionNotFound, ref)
}

// ListSessions returns every session oldest first; the default, created by no one, leads.
func (s *SessionService) ListSessions(ctx context.Context) ([]model.SessionSummary, error) {
	records, err := s.sessions.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	slices.SortStableFunc(records, func(a, b model.Session) int { return a.CreatedAt.Compare(b.CreatedAt) })

	summaries := make([]model.SessionSummary, 0, len(records))
	for _, record := range records {
		summary, err := s.summarize(ctx, record)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// CreateSession commits an empty session: its record only, nothing in the daemon.
func (s *SessionService) CreateSession(ctx context.Context, req model.CreateSessionRequest) (model.Session, error) {
	if err := req.Validate(); err != nil {
		return model.Session{}, err
	}
	return withUnlock(ctx, s.locker, func() (model.Session, error) {
		if err := s.requireFreeName(ctx, req.Name); err != nil {
			return model.Session{}, err
		}
		record := model.Session{
			ID:        model.NewSessionID(),
			Name:      req.Name,
			CreatedAt: s.now().UTC(),
		}
		if err := s.sessions.SaveSession(ctx, record); err != nil {
			return model.Session{}, fmt.Errorf("committing session %s: %w", record.ID, err)
		}
		return record, nil
	})
}

// DeleteSession drops a session and everything it holds. It refuses the default session
// and one an interrupted rollback's marker names; the running compose project is left alone.
func (s *SessionService) DeleteSession(ctx context.Context, ref string) (model.SessionID, error) {
	return withUnlock(ctx, s.locker, func() (model.SessionID, error) {
		record, err := s.ResolveSession(ctx, ref)
		if err != nil {
			return model.SessionID{}, err
		}
		id := record.ID
		if id == model.DefaultSession {
			return model.SessionID{}, model.ErrDefaultSessionDelete
		}
		marker, found, err := s.inflight.Get(ctx)
		if err != nil {
			return model.SessionID{}, fmt.Errorf("reading the inflight marker: %w", err)
		}
		if found && marker.Session == id {
			err := &model.InterruptedRollbackError{Marker: marker}
			zerolog.Ctx(ctx).Error().Err(err).Str("session", id.String()).
				Str("compose_project", marker.ComposeProject.String()).Msg(recoveryHint(marker))
			return model.SessionID{}, err
		}
		if err := s.sessions.DeleteSession(ctx, id); err != nil {
			return model.SessionID{}, fmt.Errorf("deleting session %s: %w", id, err)
		}
		zerolog.Ctx(ctx).Info().Str("session", id.String()).
			Msg("session deleted; any compose project running from it is left as it is")
		return id, nil
	})
}

// sessionByName finds the one record carrying name; several carrying it is an error.
func sessionByName(records []model.Session, name string) (model.Session, bool, error) {
	var named []model.Session
	for _, record := range records {
		if record.Name == name {
			named = append(named, record)
		}
	}
	switch len(named) {
	case 0:
		return model.Session{}, false, nil
	case 1:
		return named[0], true, nil
	default:
		ids := make([]model.SessionID, len(named))
		for i, record := range named {
			ids[i] = record.ID
		}
		return model.Session{}, false, &model.AmbiguousSessionNameError{Name: name, IDs: ids}
	}
}

// sessionByID finds the record whose id is ref.
func sessionByID(records []model.Session, ref string) (model.Session, bool) {
	for _, record := range records {
		if record.ID.String() == ref {
			return record, true
		}
	}
	return model.Session{}, false
}

// requireFreeName refuses a name another session carries, or one that is another session's
// id: ResolveSession tries names first, so such a name would hide that session's id.
func (s *SessionService) requireFreeName(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	records, err := s.sessions.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	if _, found, err := sessionByName(records, name); found || err != nil {
		return fmt.Errorf("%w: %q", model.ErrSessionNameTaken, name)
	}
	if record, found := sessionByID(records, name); found {
		return fmt.Errorf("%w: %q is the id of session %q", model.ErrSessionNameTaken, name, record.Name)
	}
	return nil
}

// summarize counts what the session holds; safety snapshots are not counted.
func (s *SessionService) summarize(ctx context.Context, record model.Session) (model.SessionSummary, error) {
	groups, err := s.sessions.ListSessionSavepoints(ctx, record.ID)
	if err != nil {
		return model.SessionSummary{}, fmt.Errorf("session %s: %w", record.ID, err)
	}
	summary := model.SessionSummary{Session: record, ComposeProjects: []model.DockerComposeProjectName{}}
	for _, group := range groups {
		summary.ComposeProjects = append(summary.ComposeProjects, group.ComposeProject)
		for _, sp := range group.Savepoints {
			if !sp.IsSafetySnapshot() {
				summary.SavepointCount++
			}
		}
	}
	return summary, nil
}
