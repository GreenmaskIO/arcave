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

// Package sessionref resolves the session a command works in, the one rule every command shares.
package sessionref

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// SessionLister lists an arcave project's committed sessions, the default included.
type SessionLister interface {
	ListSessions(ctx context.Context) ([]model.Session, error)
}

// Listed is a SessionLister over sessions already listed.
type Listed []model.Session

// ListSessions returns the sessions l holds.
func (l Listed) ListSessions(context.Context) ([]model.Session, error) { return l, nil }

// CurrentSessionReader reads the current-session pointer.
type CurrentSessionReader interface {
	Get(ctx context.Context) (id model.SessionID, found bool, err error)
}

// Resolve finds the session ref names: by name first, then by id. "" is the current session, or the
// default when no pointer is set, listing nothing; a pointer to a deleted session and a keyword ref
// are refused.
func Resolve(
	ctx context.Context, sessions SessionLister, current CurrentSessionReader, ref string,
) (model.Session, error) {
	session, _, err := ResolveCurrent(ctx, sessions, current, model.CurrentSessionRef{Ref: ref})
	return session, err
}

// ResolveCurrent finds the session a command works in when it names none, as Resolve does, and
// where it was named.
func ResolveCurrent(
	ctx context.Context, sessions SessionLister, current CurrentSessionReader, ref model.CurrentSessionRef,
) (model.Session, model.CurrentSessionSource, error) {
	if model.IsSessionRefKeyword(ref.Ref) {
		return model.Session{}, model.CurrentSessionSource{}, fmt.Errorf("%w: %q", model.ErrSessionRefKeyword, ref.Ref)
	}
	if ref.Ref != "" {
		listed, err := listSessions(ctx, sessions)
		if err != nil {
			return model.Session{}, model.CurrentSessionSource{}, err
		}
		session, err := model.FindSessionByRef(listed, ref.Ref)
		if err != nil {
			return model.Session{}, model.CurrentSessionSource{}, err
		}
		logResolved(ctx, session, "ref", ref.Ref)
		var overrides *model.SessionRef
		if ref.Source.IsOverride() {
			overrides = FindOverridden(ctx, listed, current, session.ID)
		}
		return session, model.CurrentSessionSource{
			Source:    ref.Source,
			Overrides: overrides,
		}, nil
	}
	id, found, err := ReadCurrent(ctx, current)
	if err != nil {
		return model.Session{}, model.CurrentSessionSource{}, err
	}
	if !found {
		logResolved(ctx, model.DefaultSessionRecord, "default", "")
		return model.DefaultSessionRecord, model.CurrentSessionSource{Source: model.SessionRefSourceDefault}, nil
	}
	listed, err := listSessions(ctx, sessions)
	if err != nil {
		return model.Session{}, model.CurrentSessionSource{}, err
	}
	if session, found := model.FindSessionByID(listed, id.String()); found {
		logResolved(ctx, session, "pointer", "")
		return session, model.CurrentSessionSource{Source: model.SessionRefSourcePointer}, nil
	}
	return model.Session{}, model.CurrentSessionSource{}, NewCurrentNotFoundError(id)
}

// FindOverridden is the session the pointer, else the default, names among sessions, when a flag or
// the environment chose another; nil when it is chosen, or the pointer cannot be read or followed.
func FindOverridden(
	ctx context.Context, sessions []model.Session, current CurrentSessionReader, chosen model.SessionID,
) *model.SessionRef {
	id, found, err := ReadCurrent(ctx, current)
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("the overridden session is unknown")
		return nil
	}
	session := model.DefaultSessionRecord
	if found {
		if session, found = model.FindSessionByID(sessions, id.String()); !found {
			zerolog.Ctx(ctx).Debug().Err(NewCurrentNotFoundError(id)).Msg("the overridden session is unknown")
			return nil
		}
	}
	if session.ID == chosen {
		return nil
	}
	return &model.SessionRef{
		ID:   session.ID,
		Name: session.Name,
	}
}

// NewCurrentNotFoundError is a current-session pointer naming id, a session that no longer exists.
func NewCurrentNotFoundError(id model.SessionID) error {
	return fmt.Errorf("%w: %s, named by %s/current_session", model.ErrCurrentSessionNotFound, id, model.StateDir)
}

// ReadCurrent reads the current-session pointer.
func ReadCurrent(ctx context.Context, current CurrentSessionReader) (model.SessionID, bool, error) {
	id, found, err := current.Get(ctx)
	if err != nil {
		return model.SessionID{}, false, err
	}
	zerolog.Ctx(ctx).Debug().Bool("found", found).Str("session", id.String()).Msg("current-session pointer read")
	return id, found, nil
}

// listSessions lists the sessions a ref is looked up among.
func listSessions(ctx context.Context, sessions SessionLister) ([]model.Session, error) {
	listed, err := sessions.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	return listed, nil
}

// logResolved records which session a command works in and what named it: a ref, the pointer
// or the default.
func logResolved(ctx context.Context, session model.Session, by, ref string) {
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Str("name", session.Name).
		Str("by", by).Str("ref", ref).Msg("session resolved")
}
