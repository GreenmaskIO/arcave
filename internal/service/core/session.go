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
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

type sessionRepository interface {
	SaveSession(ctx context.Context, session model.Session) error
	CopySession(ctx context.Context, from, to model.SessionID, except ...string) (int64, error)
	IsSessionTreeEmpty(ctx context.Context, id model.SessionID) (bool, error)
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
	ListSessions(ctx context.Context) ([]model.Session, error)
	DeleteSession(ctx context.Context, id model.SessionID) error
	ListSessionSavepoints(ctx context.Context, id model.SessionID) ([]model.ComposeProjectSavepoints, error)
	SessionSize(ctx context.Context, id model.SessionID) (int64, error)
}

type heartbeatWriter interface {
	Write(ctx context.Context, h heartbeat.Heartbeat) error
}

// SessionDeps are SessionService's collaborators.
type SessionDeps struct {
	Sessions sessionRepository
	Locker   projectLocker
	Inflight inflightMarkers
	// Store is the arcave project store a fork's heartbeat is written into.
	Store storages.Storager
	// RunnerInfo identifies this process in a fork's heartbeat.
	RunnerInfo heartbeat.RunnerInfo
	// HeartbeatInterval and HeartbeatStaleAfter time a fork's heartbeat; zero keeps
	// heartbeat.DefaultInterval and heartbeat.DefaultStaleAfter.
	HeartbeatInterval   time.Duration
	HeartbeatStaleAfter time.Duration
}

// SessionService manages an arcave project's sessions. It touches no daemon and names
// no compose project.
type SessionService struct {
	sessions   sessionRepository
	locker     projectLocker
	inflight   inflightMarkers
	runnerInfo heartbeat.RunnerInfo
	// heartbeatInterval and heartbeatStaleAfter time a fork's heartbeat.
	heartbeatInterval   time.Duration
	heartbeatStaleAfter time.Duration
	// heartbeatWriter opens the heartbeat of one session's tree.
	heartbeatWriter func(id model.SessionID) (heartbeatWriter, error)
}

func NewSessionService(d SessionDeps) *SessionService {
	return &SessionService{
		sessions:            d.Sessions,
		locker:              d.Locker,
		inflight:            d.Inflight,
		runnerInfo:          d.RunnerInfo,
		heartbeatInterval:   cmp.Or(d.HeartbeatInterval, heartbeat.DefaultInterval),
		heartbeatStaleAfter: cmp.Or(d.HeartbeatStaleAfter, heartbeat.DefaultStaleAfter),
		heartbeatWriter: func(id model.SessionID) (heartbeatWriter, error) {
			st, err := storefactory.Session(d.Store, id)
			if err != nil {
				return nil, err
			}
			return heartbeat.NewWriter(st), nil
		},
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
	return findSessionByRef(records, ref)
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

// CreateSession commits a session, empty or a clone of req.From: every compose project and
// savepoint copied, the record written last. The daemon is never touched.
func (s *SessionService) CreateSession(ctx context.Context, req model.CreateSessionRequest) (model.Session, error) {
	if err := req.Validate(); err != nil {
		return model.Session{}, err
	}
	return withUnlock(ctx, s.locker, func() (model.Session, error) {
		var records []model.Session
		if req.Name != "" || req.From != "" {
			var err error
			if records, err = s.sessions.ListSessions(ctx); err != nil {
				return model.Session{}, fmt.Errorf("listing sessions: %w", err)
			}
		}
		if err := requireFreeName(records, req.Name); err != nil {
			return model.Session{}, err
		}
		record := model.Session{
			ID:        model.NewSessionID(),
			Name:      req.Name,
			CreatedAt: time.Now().UTC(),
		}
		if req.From == "" {
			if err := s.sessions.SaveSession(ctx, record); err != nil {
				return model.Session{}, fmt.Errorf("committing session %s: %w", record.ID, err)
			}
			return record, nil
		}
		source, err := findSessionByRef(records, req.From)
		if err != nil {
			return model.Session{}, err
		}
		record.From = &model.SessionOrigin{Session: source.ID}
		if err := s.forkSession(ctx, record); err != nil {
			return model.Session{}, err
		}
		return record, nil
	})
}

// DeleteSession drops a session and everything it holds, and reports the bytes that freed. It
// refuses the default session and one an interrupted rollback's marker names; the running
// compose project is left alone.
func (s *SessionService) DeleteSession(ctx context.Context, ref string) (model.DeleteSessionResponse, error) {
	return withUnlock(ctx, s.locker, func() (model.DeleteSessionResponse, error) {
		record, err := s.ResolveSession(ctx, ref)
		if err != nil {
			return model.DeleteSessionResponse{}, err
		}
		id := record.ID
		if id == model.DefaultSession {
			return model.DeleteSessionResponse{}, model.ErrDefaultSessionDelete
		}
		marker, found, err := s.inflight.Get(ctx)
		if err != nil {
			return model.DeleteSessionResponse{}, fmt.Errorf("reading the inflight marker: %w", err)
		}
		if found && marker.Session == id {
			err := &model.InterruptedRollbackError{Marker: marker}
			zerolog.Ctx(ctx).Error().Err(err).Str("session", id.String()).
				Str("compose_project", marker.ComposeProject.String()).Msg(recoveryHint(marker))
			return model.DeleteSessionResponse{}, err
		}
		size, err := s.sessions.SessionSize(ctx, id)
		if err != nil {
			return model.DeleteSessionResponse{}, err
		}
		if err := s.sessions.DeleteSession(ctx, id); err != nil {
			return model.DeleteSessionResponse{}, fmt.Errorf("deleting session %s: %w", id, err)
		}
		zerolog.Ctx(ctx).Info().Str("session", id.String()).Int64("freed_bytes", size).
			Msg("session deleted; any compose project running from it is left as it is")
		return model.DeleteSessionResponse{
			Session:    id,
			Deleted:    true,
			FreedBytes: size,
		}, nil
	})
}

// findSessionByRef finds the record ref names among records: by name first, then by id.
func findSessionByRef(records []model.Session, ref string) (model.Session, error) {
	if record, found, err := findSessionByName(records, ref); err != nil || found {
		return record, err
	}
	if record, found := findSessionByID(records, ref); found {
		return record, nil
	}
	return model.Session{}, fmt.Errorf("%w: %q", model.ErrSessionNotFound, ref)
}

// findSessionByName finds the one record carrying name; several carrying it is an error.
func findSessionByName(records []model.Session, name string) (model.Session, bool, error) {
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

// findSessionByID finds the record whose id is ref.
func findSessionByID(records []model.Session, ref string) (model.Session, bool) {
	for _, record := range records {
		if record.ID.String() == ref {
			return record, true
		}
	}
	return model.Session{}, false
}

// summarize counts what the session holds, safety snapshots excluded, and measures its tree.
func (s *SessionService) summarize(ctx context.Context, record model.Session) (model.SessionSummary, error) {
	groups, err := s.sessions.ListSessionSavepoints(ctx, record.ID)
	if err != nil {
		return model.SessionSummary{}, fmt.Errorf("session %s: %w", record.ID, err)
	}
	size, err := s.sessions.SessionSize(ctx, record.ID)
	if err != nil {
		return model.SessionSummary{}, err
	}
	summary := model.SessionSummary{Session: record, ComposeProjects: []model.DockerComposeProjectName{}, SizeBytes: size}
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

// requireFreeName refuses a name another session carries, or one that is another session's
// id: ResolveSession tries names first, so such a name would hide that session's id.
func requireFreeName(records []model.Session, name string) error {
	if name == "" {
		return nil
	}
	if _, found, err := findSessionByName(records, name); found || err != nil {
		return fmt.Errorf("%w: %q", model.ErrSessionNameTaken, name)
	}
	if record, found := findSessionByID(records, name); found {
		return fmt.Errorf("%w: %q is the id of session %q", model.ErrSessionNameTaken, name, record.Name)
	}
	return nil
}

// forkSession copies record.From's tree under record.ID and commits record, a heartbeat kept
// beside the tree throughout. A failure leaves the tree to a gc, its heartbeat marked failed.
func (s *SessionService) forkSession(ctx context.Context, record model.Session) error {
	source := record.From.Session
	if err := s.requireEmptyForkTree(ctx, record.ID); err != nil {
		return fmt.Errorf("cloning session %s: %w", source, err)
	}
	data, err := heartbeat.EncodeData(model.ForkHeartbeatData{From: source})
	if err != nil {
		return err
	}
	w, err := s.heartbeatWriter(record.ID)
	if err != nil {
		return fmt.Errorf("cloning session %s: open the heartbeat: %w", source, err)
	}
	copyCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	hb := heartbeat.NewWorker(w).SetInterval(s.heartbeatInterval).SetStaleAfter(s.heartbeatStaleAfter)
	base := heartbeat.Heartbeat{Operation: model.HeartbeatOperationFork, RunnerInfo: s.runnerInfo, Data: data}
	if err := hb.Start(ctx, base, cancel); err != nil {
		// A write that timed out may still have landed: mark it failed rather than leave it in progress.
		err = fmt.Errorf("cloning session %s: %w", source, err)
		abandonFork(ctx, hb, record.ID, err)
		return err
	}
	copied, err := s.sessions.CopySession(copyCtx, source, record.ID, heartbeat.FileName)
	hb.Stop()
	if err != nil {
		if cause := context.Cause(copyCtx); errors.Is(cause, model.ErrSessionHeartbeatLost) {
			err = cause
		}
		err = fmt.Errorf("cloning session %s: %w", source, err)
		abandonFork(ctx, hb, record.ID, err)
		return err
	}
	zerolog.Ctx(ctx).Info().Str("session", record.ID.String()).Str("from", source.String()).
		Int64("copied_bytes", copied).Msg("session cloned")

	if err := hb.Alive(); err != nil {
		err = fmt.Errorf("committing session %s: %w", record.ID, err)
		abandonFork(ctx, hb, record.ID, err)
		return err
	}
	if err := s.sessions.SaveSession(ctx, record); err != nil {
		err = fmt.Errorf("committing session %s: %w", record.ID, err)
		abandonFork(ctx, hb, record.ID, err)
		return err
	}
	hb.Finish(ctx)
	return nil
}

// requireEmptyForkTree refuses a fork's tree that holds anything, a heartbeat included.
func (s *SessionService) requireEmptyForkTree(ctx context.Context, id model.SessionID) error {
	empty, err := s.sessions.IsSessionTreeEmpty(ctx, id)
	if err != nil {
		return fmt.Errorf("checking the tree of session %s: %w", id, err)
	}
	if !empty {
		return fmt.Errorf("%w: sessions/%s", model.ErrSessionTreeOccupied, id)
	}
	return nil
}

// abandonFork marks an uncommitted fork failed and logs its tree, the only pointer left to it.
func abandonFork(ctx context.Context, hb *heartbeat.Worker, id model.SessionID, cause error) {
	hb.Fail(ctx, cause)
	zerolog.Ctx(ctx).Error().Str("session", id.String()).Str("tree", "sessions/"+id.String()).
		Msg("the fork did not commit; its tree is left in the store")
}
