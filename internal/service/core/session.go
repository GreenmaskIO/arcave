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
	ListUncommittedSessions(ctx context.Context) ([]model.SessionID, error)
	DeleteSession(ctx context.Context, id model.SessionID) error
	SessionSize(ctx context.Context, id model.SessionID) (int64, error)
}

// currentSessionPointer is .arcave/current_session: the session a command works in when it
// names none.
type currentSessionPointer interface {
	Get(ctx context.Context) (id model.SessionID, found bool, err error)
	Put(ctx context.Context, id model.SessionID) error
	Clear(ctx context.Context) error
}

type heartbeatWriter interface {
	Write(ctx context.Context, beat heartbeat.Heartbeat) error
}

type heartbeatReader interface {
	Read(ctx context.Context) (heartbeat.Heartbeat, error)
}

// SessionDeps are SessionService's collaborators.
type SessionDeps struct {
	Sessions sessionRepository
	Locker   projectLocker
	Inflight inflightMarkers
	// Current is the current-session pointer.
	Current currentSessionPointer
	// Store is the arcave project store a fork's heartbeat is written into and read from, and a
	// session's savepoints are listed off.
	Store storages.Navigator
	// NewSavepoints opens the savepoints of a compose project's store.
	NewSavepoints func(store storages.Storager) SavepointReader
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
	current    currentSessionPointer
	runnerInfo heartbeat.RunnerInfo
	// heartbeatInterval and heartbeatStaleAfter time a fork's heartbeat.
	heartbeatInterval   time.Duration
	heartbeatStaleAfter time.Duration
	// heartbeatWriter and heartbeatReader open the heartbeat of one session's tree.
	heartbeatWriter func(id model.SessionID) (heartbeatWriter, error)
	heartbeatReader func(id model.SessionID) (heartbeatReader, error)
	// sessionSavepoints lists one session's committed savepoints by compose project.
	sessionSavepoints func(ctx context.Context, id model.SessionID) ([]model.ComposeProjectSavepoints, error)
}

func NewSessionService(deps SessionDeps) *SessionService {
	staleAfter := cmp.Or(deps.HeartbeatStaleAfter, heartbeat.DefaultStaleAfter)
	return &SessionService{
		sessions:            deps.Sessions,
		locker:              deps.Locker,
		inflight:            deps.Inflight,
		current:             deps.Current,
		runnerInfo:          deps.RunnerInfo,
		heartbeatInterval:   cmp.Or(deps.HeartbeatInterval, heartbeat.DefaultInterval),
		heartbeatStaleAfter: staleAfter,
		heartbeatWriter: func(id model.SessionID) (heartbeatWriter, error) {
			sessionStore, err := storefactory.Session(deps.Store, id)
			if err != nil {
				return nil, err
			}
			return heartbeat.NewWriter(sessionStore), nil
		},
		heartbeatReader: func(id model.SessionID) (heartbeatReader, error) {
			sessionStore, err := storefactory.Session(deps.Store, id)
			if err != nil {
				return nil, err
			}
			return heartbeat.NewReader(sessionStore).SetStaleAfter(staleAfter), nil
		},
		sessionSavepoints: func(ctx context.Context, id model.SessionID) ([]model.ComposeProjectSavepoints, error) {
			return readSessionSavepoints(ctx, deps.Store, deps.NewSavepoints, id)
		},
	}
}

// ResolveSession finds the session ref names: by name first, then by id. "" is the current
// session, or the default when no pointer is set; a pointer to a deleted session is refused.
func (s *SessionService) ResolveSession(ctx context.Context, ref string) (model.Session, error) {
	if ref != "" {
		sessions, err := s.sessions.ListSessions(ctx)
		if err != nil {
			return model.Session{}, fmt.Errorf("listing sessions: %w", err)
		}
		session, err := findSessionByRef(sessions, ref)
		if err != nil {
			return model.Session{}, err
		}
		logResolved(ctx, session, "ref", ref)
		return session, nil
	}
	id, found, err := s.readCurrent(ctx)
	if err != nil {
		return model.Session{}, err
	}
	if !found {
		logResolved(ctx, model.DefaultSessionRecord, "default", "")
		return model.DefaultSessionRecord, nil
	}
	sessions, err := s.sessions.ListSessions(ctx)
	if err != nil {
		return model.Session{}, fmt.Errorf("listing sessions: %w", err)
	}
	if session, found := findSessionByID(sessions, id.String()); found {
		logResolved(ctx, session, "pointer", "")
		return session, nil
	}
	return model.Session{}, fmt.Errorf("%w: %s, named by %s/current_session; "+
		"run 'arcave session unset' or 'arcave session set <session>'", model.ErrCurrentSessionNotFound, id, model.StateDir)
}

// logResolved records which session a command works in and what named it: a ref, the pointer
// or the default.
func logResolved(ctx context.Context, session model.Session, by, ref string) {
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Str("name", session.Name).
		Str("by", by).Str("ref", ref).Msg("session resolved")
}

// SetSession makes the session ref names the current one.
func (s *SessionService) SetSession(ctx context.Context, ref string) (model.Session, error) {
	if ref == "" {
		return model.Session{}, model.ErrNoSessionRef
	}
	return withUnlock(ctx, s.locker, func() (model.Session, error) {
		sessions, err := s.sessions.ListSessions(ctx)
		if err != nil {
			return model.Session{}, fmt.Errorf("listing sessions: %w", err)
		}
		session, err := findSessionByRef(sessions, ref)
		if err != nil {
			return model.Session{}, err
		}
		if err := s.current.Put(ctx, session.ID); err != nil {
			return model.Session{}, err
		}
		zerolog.Ctx(ctx).Info().Str("session", session.ID.String()).Str("name", session.Name).
			Msg("current session set")
		return session, nil
	})
}

// UnsetSession clears the current-session pointer, so commands that name no session work in
// the default one.
func (s *SessionService) UnsetSession(ctx context.Context) (model.Session, error) {
	return withUnlock(ctx, s.locker, func() (model.Session, error) {
		if err := s.current.Clear(ctx); err != nil {
			return model.Session{}, err
		}
		zerolog.Ctx(ctx).Info().Msg("current session cleared; commands now work in the default session")
		return model.DefaultSessionRecord, nil
	})
}

// ListSessions returns every session in req.Sort's order; the default, created by no one, is
// the oldest.
// req.All adds the trees without a record, each with the status its heartbeat gives it; the
// session req.Ref resolves to is marked current.
func (s *SessionService) ListSessions(ctx context.Context, req model.ListSessionsRequest) ([]model.SessionSummary, error) {
	sessions, err := s.sessions.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	current := s.findCurrent(ctx, sessions, req.Ref)
	summaries := make([]model.SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		summary, err := s.summarize(ctx, session)
		if err != nil {
			return nil, err
		}
		summary.Status = model.SessionStatusReady
		summary.Current = session.ID == current
		summaries = append(summaries, summary)
	}
	if req.All {
		uncommitted, err := s.listUncommitted(ctx)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, uncommitted...)
	}
	zerolog.Ctx(ctx).Debug().Int("committed", len(sessions)).Int("listed", len(summaries)).
		Str("current", current.String()).Bool("all", req.All).Msg("sessions listed")
	slices.SortStableFunc(summaries, func(a, b model.SessionSummary) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if req.Sort != model.SortAsc {
		slices.Reverse(summaries)
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
		var sessions []model.Session
		if req.Name != "" || req.From != "" {
			var err error
			if sessions, err = s.sessions.ListSessions(ctx); err != nil {
				return model.Session{}, fmt.Errorf("listing sessions: %w", err)
			}
		}
		if err := requireFreeName(sessions, req.Name); err != nil {
			return model.Session{}, err
		}
		session := model.Session{
			ID:        model.NewSessionID(),
			Name:      req.Name,
			CreatedAt: time.Now().UTC(),
		}
		if req.From == "" {
			if err := s.sessions.SaveSession(ctx, session); err != nil {
				return model.Session{}, fmt.Errorf("committing session %s: %w", session.ID, err)
			}
			zerolog.Ctx(ctx).Info().Str("session", session.ID.String()).Str("name", session.Name).
				Msg("session created")
			return session, nil
		}
		source, err := findSessionByRef(sessions, req.From)
		if err != nil {
			return model.Session{}, err
		}
		session.From = &model.SessionOrigin{Session: source.ID}
		zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Str("name", session.Name).
			Str("from", source.ID.String()).Str("from_name", source.Name).Msg("forking session")
		if err := s.forkSession(ctx, session); err != nil {
			return model.Session{}, err
		}
		return session, nil
	})
}

// RemoveSession drops a session and everything it holds, and reports the bytes freed. It refuses
// the default session and one an inflight marker names; the current or pinned session, and a tree
// still cloning or unknown, only unless forced. Forcing the current one clears the pointer.
func (s *SessionService) RemoveSession(
	ctx context.Context, req model.RemoveSessionRequest,
) (model.RemoveSessionResponse, error) {
	if req.Session == "" {
		return model.RemoveSessionResponse{}, model.ErrNoSessionRef
	}
	return withUnlock(ctx, s.locker, func() (model.RemoveSessionResponse, error) {
		id, err := s.findRemovable(ctx, req)
		if err != nil {
			return model.RemoveSessionResponse{}, err
		}
		zerolog.Ctx(ctx).Debug().Str("ref", req.Session).Str("session", id.String()).Bool("force", req.Force).
			Msg("session to delete found")
		if id == model.DefaultSession {
			return model.RemoveSessionResponse{}, model.ErrDefaultSessionDelete
		}
		marker, found, err := s.inflight.Get(ctx)
		if err != nil {
			return model.RemoveSessionResponse{}, fmt.Errorf("reading the inflight marker: %w", err)
		}
		if found && marker.Session == id {
			err := &model.InterruptedRollbackError{Marker: marker}
			zerolog.Ctx(ctx).Error().Err(err).Str("session", id.String()).
				Str("compose_project", marker.ComposeProject.String()).Msg(recoveryHint(marker))
			return model.RemoveSessionResponse{}, err
		}
		current, err := s.requireNotInUse(ctx, id, req)
		if err != nil {
			return model.RemoveSessionResponse{}, err
		}
		size, err := s.sessions.SessionSize(ctx, id)
		if err != nil {
			return model.RemoveSessionResponse{}, err
		}
		zerolog.Ctx(ctx).Debug().Str("session", id.String()).Int64("size_bytes", size).Msg("deleting session")
		if err := s.sessions.DeleteSession(ctx, id); err != nil {
			logInterrupt(ctx, "session remove interrupted: run it again with the session id to remove the rest")
			return model.RemoveSessionResponse{}, fmt.Errorf("deleting session %s: %w", id, err)
		}
		zerolog.Ctx(ctx).Info().Str("session", id.String()).Int64("freed_bytes", size).
			Msg("session deleted; any compose project running from it is left as it is")
		if current {
			if err := s.current.Clear(ctx); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Str("session", id.String()).
					Msg("the deleted session is still the current one; run 'arcave session unset'")
			} else {
				zerolog.Ctx(ctx).Info().Str("session", id.String()).
					Msg("the deleted session was the current one; commands now work in the default session")
			}
		}
		return model.RemoveSessionResponse{
			Session:    id,
			Removed:    true,
			FreedBytes: size,
		}, nil
	})
}

// findRemovable resolves req.Session to a session, or by id to a tree without a record; a tree
// still cloning or of unknown status is refused unless req.Force.
func (s *SessionService) findRemovable(ctx context.Context, req model.RemoveSessionRequest) (model.SessionID, error) {
	session, err := s.ResolveSession(ctx, req.Session)
	if !errors.Is(err, model.ErrSessionNotFound) {
		return session.ID, err
	}
	id, parseErr := model.ParseSessionID(req.Session)
	if parseErr != nil {
		return model.SessionID{}, err
	}
	zerolog.Ctx(ctx).Debug().Str("session", id.String()).Msg("no session record; looking for a tree without one")
	uncommitted, listErr := s.sessions.ListUncommittedSessions(ctx)
	if listErr != nil {
		return model.SessionID{}, fmt.Errorf("listing sessions: %w", listErr)
	}
	if !slices.Contains(uncommitted, id) {
		return model.SessionID{}, err
	}
	status, _, readErr := s.readTreeStatus(ctx, id)
	if !req.Force {
		switch status {
		case model.SessionStatusCloning:
			return model.SessionID{}, fmt.Errorf("%w: %s's heartbeat is younger than its stale age; "+
				"if no fork is running, --force deletes it", model.ErrSessionCloning, id)
		case model.SessionStatusUnknown:
			return model.SessionID{}, fmt.Errorf("%w: %s: %w; if nothing is writing it, --force deletes it",
				model.ErrSessionStatusUnknown, id, readErr)
		}
	}
	zerolog.Ctx(ctx).Info().Str("session", id.String()).Str("status", string(status)).
		Msg("deleting a session tree without a record")
	return id, nil
}

// requireNotInUse refuses id when it is the current or the pinned session, unless req.Force; it
// reports whether id is the current one. An unreadable pointer names no session.
func (s *SessionService) requireNotInUse(
	ctx context.Context, id model.SessionID, req model.RemoveSessionRequest,
) (bool, error) {
	pointer, found := s.readCurrentOrNone(ctx)
	current := found && pointer == id
	if req.Force {
		if current || req.Pinned == id {
			zerolog.Ctx(ctx).Debug().Str("session", id.String()).Bool("current", current).
				Bool("pinned", req.Pinned == id).Msg("deleting a session in use, forced")
		}
		return current, nil
	}
	if current {
		return false, fmt.Errorf("%w: %s; switch with 'arcave session set <session>' or 'arcave session unset' "+
			"first, or force it to delete it and clear the pointer", model.ErrCurrentSessionDelete, id)
	}
	if req.Pinned == id {
		return false, fmt.Errorf("%w: %s is the session this MCP server is pinned to; force it to delete it anyway",
			model.ErrCurrentSessionDelete, id)
	}
	return false, nil
}

// readCurrent reads the current-session pointer, naming the way out of an unreadable one.
func (s *SessionService) readCurrent(ctx context.Context) (model.SessionID, bool, error) {
	id, found, err := s.current.Get(ctx)
	if err != nil {
		return model.SessionID{}, false, fmt.Errorf("%w; run 'arcave session unset' or 'arcave session set <session>'", err)
	}
	zerolog.Ctx(ctx).Debug().Bool("found", found).Str("session", id.String()).Msg("current-session pointer read")
	return id, found, nil
}

// readCurrentOrNone reads the pointer for a command it only informs: an unreadable one is
// logged and read as none.
func (s *SessionService) readCurrentOrNone(ctx context.Context) (model.SessionID, bool) {
	id, found, err := s.readCurrent(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("reading the current session")
		return model.SessionID{}, false
	}
	return id, found
}

// findCurrent is the session ref resolves to among sessions, or the current session for ""; zero
// when ref names none or the pointer cannot be read.
func (s *SessionService) findCurrent(ctx context.Context, sessions []model.Session, ref string) model.SessionID {
	if ref != "" {
		session, err := findSessionByRef(sessions, ref)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("marking the current session")
			return model.SessionID{}
		}
		return session.ID
	}
	id, found, err := s.readCurrent(ctx)
	switch {
	case err != nil:
		zerolog.Ctx(ctx).Warn().Err(err).Msg("reading the current session")
		return model.SessionID{}
	case !found:
		return model.DefaultSession
	default:
		return id
	}
}

// findSessionByRef finds the session ref names among sessions: by name first, then by id.
func findSessionByRef(sessions []model.Session, ref string) (model.Session, error) {
	if session, found, err := findSessionByName(sessions, ref); err != nil || found {
		return session, err
	}
	if session, found := findSessionByID(sessions, ref); found {
		return session, nil
	}
	return model.Session{}, fmt.Errorf("%w: %q", model.ErrSessionNotFound, ref)
}

// findSessionByName finds the one session carrying name; several carrying it is an error.
func findSessionByName(sessions []model.Session, name string) (model.Session, bool, error) {
	var named []model.Session
	for _, session := range sessions {
		if session.Name == name {
			named = append(named, session)
		}
	}
	switch len(named) {
	case 0:
		return model.Session{}, false, nil
	case 1:
		return named[0], true, nil
	default:
		ids := make([]model.SessionID, len(named))
		for i, session := range named {
			ids[i] = session.ID
		}
		return model.Session{}, false, &model.AmbiguousSessionNameError{Name: name, IDs: ids}
	}
}

// findSessionByID finds the session whose id is ref.
func findSessionByID(sessions []model.Session, ref string) (model.Session, bool) {
	for _, session := range sessions {
		if session.ID.String() == ref {
			return session, true
		}
	}
	return model.Session{}, false
}

// summarize counts what the session holds, safety snapshots excluded, and measures its tree.
func (s *SessionService) summarize(ctx context.Context, session model.Session) (model.SessionSummary, error) {
	groups, err := s.sessionSavepoints(ctx, session.ID)
	if err != nil {
		return model.SessionSummary{}, fmt.Errorf("session %s: %w", session.ID, err)
	}
	size, err := s.sessions.SessionSize(ctx, session.ID)
	if err != nil {
		return model.SessionSummary{}, err
	}
	var (
		composeProjects = []model.DockerComposeProjectName{}
		savepointCount  int
	)
	for _, group := range groups {
		composeProjects = append(composeProjects, group.ComposeProject)
		for _, savepoint := range group.Savepoints {
			if !savepoint.IsSafetySnapshot() {
				savepointCount++
			}
		}
	}
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Int("compose_projects", len(groups)).
		Int("savepoints", savepointCount).Int64("size_bytes", size).Msg("session summarized")
	return model.SessionSummary{
		Session:         session,
		ComposeProjects: composeProjects,
		SavepointCount:  savepointCount,
		SizeBytes:       size,
	}, nil
}

// readSessionSavepoints lists every committed savepoint of session id, grouped by compose
// project and ordered by its name; a compose project holding none is left out.
func readSessionSavepoints(
	ctx context.Context, store storages.Navigator, newSavepoints func(storages.Storager) SavepointReader,
	id model.SessionID,
) ([]model.ComposeProjectSavepoints, error) {
	sessionStore, err := storefactory.Session(store, id)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", id, err)
	}
	names, err := storefactory.ComposeProjects(ctx, sessionStore)
	if err != nil {
		return nil, fmt.Errorf("list compose projects of session %s: %w", id, err)
	}
	groups := make([]model.ComposeProjectSavepoints, 0, len(names))
	for _, name := range names {
		composeProjectStore, err := storefactory.ComposeProject(sessionStore, name)
		if err != nil {
			return nil, fmt.Errorf("open compose project %s: %w", name, err)
		}
		savepoints, err := newSavepoints(composeProjectStore).ListSavepoints(ctx)
		if err != nil {
			return nil, fmt.Errorf("list savepoints of compose project %s: %w", name, err)
		}
		if len(savepoints) == 0 {
			continue
		}
		groups = append(groups, model.ComposeProjectSavepoints{
			ComposeProject: name,
			Savepoints:     savepoints,
		})
	}
	return groups, nil
}

// listUncommitted summarizes every tree without a record, with what its heartbeat says of it.
func (s *SessionService) listUncommitted(ctx context.Context) ([]model.SessionSummary, error) {
	ids, err := s.sessions.ListUncommittedSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	zerolog.Ctx(ctx).Debug().Int("trees", len(ids)).Msg("session trees without a record found")
	summaries := make([]model.SessionSummary, 0, len(ids))
	for _, id := range ids {
		status, beat, readErr := s.readTreeStatus(ctx, id)
		session := model.Session{ID: id, CreatedAt: beat.StartedAt, From: newForkOrigin(beat)}
		summary, err := s.summarize(ctx, session)
		if err != nil {
			// A tree still being written may hold a half-copied record; keep its row.
			zerolog.Ctx(ctx).Warn().Err(err).Str("session", id.String()).Msg("summarizing a session tree without a record")
			summary = model.SessionSummary{Session: session, ComposeProjects: []model.DockerComposeProjectName{}}
		}
		summary.Status = status
		switch status {
		case model.SessionStatusFailed:
			summary.Error = beat.Error
		case model.SessionStatusUnknown:
			summary.Error = readErr.Error()
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// readTreeStatus derives a recordless tree's status from its heartbeat; an unknown status comes
// with the reason.
func (s *SessionService) readTreeStatus(
	ctx context.Context, id model.SessionID,
) (model.SessionStatus, heartbeat.Heartbeat, error) {
	reader, err := s.heartbeatReader(id)
	if err != nil {
		return model.SessionStatusUnknown, heartbeat.Heartbeat{}, fmt.Errorf("opening the heartbeat: %w", err)
	}
	beat, err := reader.Read(ctx)
	if errors.Is(err, model.ErrHeartbeatNotFound) {
		zerolog.Ctx(ctx).Debug().Str("session", id.String()).Msg("tree has no heartbeat")
		return model.SessionStatusIncomplete, heartbeat.Heartbeat{}, nil
	}
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session", id.String()).Msg("reading the heartbeat")
		return model.SessionStatusUnknown, heartbeat.Heartbeat{}, err
	}
	zerolog.Ctx(ctx).Debug().Str("session", id.String()).Str("operation", string(beat.Operation)).
		Str("status", string(beat.Status)).Time("updated_at", beat.UpdatedAt).Msg("tree heartbeat read")
	switch {
	case beat.Status == heartbeat.StatusDone:
		return model.SessionStatusIncomplete, beat, nil
	case beat.Status == heartbeat.StatusInProgress && beat.Operation == model.HeartbeatOperationFork:
		return model.SessionStatusCloning, beat, nil
	case beat.Status == heartbeat.StatusInProgress:
		return model.SessionStatusUnknown, beat, fmt.Errorf("a %q operation is in progress", beat.Operation)
	default:
		return model.SessionStatusFailed, beat, nil
	}
}

// newForkOrigin is the session a fork's heartbeat names as its source; nil for any other heartbeat.
func newForkOrigin(beat heartbeat.Heartbeat) *model.SessionOrigin {
	if beat.Operation != model.HeartbeatOperationFork {
		return nil
	}
	forkData, err := heartbeat.DecodeData[model.ForkHeartbeatData](beat)
	if err != nil {
		return nil
	}
	return &model.SessionOrigin{Session: forkData.From}
}

// requireFreeName refuses a name another session carries, or one that is another session's
// id: ResolveSession tries names first, so such a name would hide that session's id.
func requireFreeName(sessions []model.Session, name string) error {
	if name == "" {
		return nil
	}
	if _, found, err := findSessionByName(sessions, name); found || err != nil {
		return fmt.Errorf("%w: %q", model.ErrSessionNameTaken, name)
	}
	if session, found := findSessionByID(sessions, name); found {
		return fmt.Errorf("%w: %q is the id of session %q", model.ErrSessionNameTaken, name, session.Name)
	}
	return nil
}

// forkSession copies session.From's tree under session.ID and commits session, a heartbeat kept
// beside the tree throughout. A failure leaves the tree to a gc, its heartbeat marked failed.
func (s *SessionService) forkSession(ctx context.Context, session model.Session) error {
	source := session.From.Session
	if err := s.requireEmptyForkTree(ctx, session.ID); err != nil {
		return fmt.Errorf("cloning session %s: %w", source, err)
	}
	forkData, err := heartbeat.EncodeData(model.ForkHeartbeatData{From: source})
	if err != nil {
		return err
	}
	writer, err := s.heartbeatWriter(session.ID)
	if err != nil {
		return fmt.Errorf("cloning session %s: open the heartbeat: %w", source, err)
	}
	copyCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	worker := heartbeat.NewWorker(writer).SetInterval(s.heartbeatInterval).SetStaleAfter(s.heartbeatStaleAfter)
	initialBeat := heartbeat.Heartbeat{Operation: model.HeartbeatOperationFork, RunnerInfo: s.runnerInfo, Data: forkData}
	if err := worker.Start(ctx, initialBeat, cancel); err != nil {
		// A write that timed out may still have landed: mark it failed rather than leave it in progress.
		err = fmt.Errorf("cloning session %s: %w", source, err)
		abandonFork(ctx, worker, session.ID, err)
		return err
	}
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Str("from", source.String()).
		Msg("heartbeat started; copying the source tree")
	copied, err := s.sessions.CopySession(copyCtx, source, session.ID, heartbeat.FileName)
	worker.Stop()
	if err != nil {
		if cause := context.Cause(copyCtx); errors.Is(cause, model.ErrHeartbeatLost) {
			err = cause
		}
		err = fmt.Errorf("cloning session %s: %w", source, err)
		abandonFork(ctx, worker, session.ID, err)
		return err
	}
	zerolog.Ctx(ctx).Info().Str("session", session.ID.String()).Str("from", source.String()).
		Int64("copied_bytes", copied).Msg("session cloned")

	if err := worker.Alive(); err != nil {
		err = fmt.Errorf("committing session %s: %w", session.ID, err)
		abandonFork(ctx, worker, session.ID, err)
		return err
	}
	if err := s.sessions.SaveSession(ctx, session); err != nil {
		err = fmt.Errorf("committing session %s: %w", session.ID, err)
		abandonFork(ctx, worker, session.ID, err)
		return err
	}
	worker.Finish(ctx)
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Msg("fork committed")
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
func abandonFork(ctx context.Context, worker *heartbeat.Worker, id model.SessionID, cause error) {
	logInterrupt(ctx, "fork interrupted: the partial session is not committed")
	worker.Fail(ctx, cause)
	zerolog.Ctx(ctx).Error().Str("session", id.String()).Str("tree", "sessions/"+id.String()).
		Msg("the fork did not commit; its tree is left in the store")
}
