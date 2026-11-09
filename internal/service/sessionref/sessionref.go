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

// CurrentSessionReader reads the current-session pointer.
type CurrentSessionReader interface {
	Get(ctx context.Context) (id model.SessionID, found bool, err error)
}

// Resolve finds the session ref names: by name first, then by id. "" is the current session, or
// the default when no pointer is set; a pointer to a deleted session is refused.
func Resolve(
	ctx context.Context, sessions SessionLister, current CurrentSessionReader, ref string,
) (model.Session, error) {
	if ref != "" {
		listed, err := sessions.ListSessions(ctx)
		if err != nil {
			return model.Session{}, fmt.Errorf("listing sessions: %w", err)
		}
		session, err := model.FindSessionByRef(listed, ref)
		if err != nil {
			return model.Session{}, err
		}
		logResolved(ctx, session, "ref", ref)
		return session, nil
	}
	id, found, err := ReadCurrent(ctx, current)
	if err != nil {
		return model.Session{}, err
	}
	if !found {
		logResolved(ctx, model.DefaultSessionRecord, "default", "")
		return model.DefaultSessionRecord, nil
	}
	listed, err := sessions.ListSessions(ctx)
	if err != nil {
		return model.Session{}, fmt.Errorf("listing sessions: %w", err)
	}
	if session, found := model.FindSessionByID(listed, id.String()); found {
		logResolved(ctx, session, "pointer", "")
		return session, nil
	}
	return model.Session{}, fmt.Errorf("%w: %s, named by %s/current_session; "+
		"run 'arcave session unset' or 'arcave session set <session>'", model.ErrCurrentSessionNotFound, id, model.StateDir)
}

// ReadCurrent reads the current-session pointer, naming the way out of an unreadable one.
func ReadCurrent(ctx context.Context, current CurrentSessionReader) (model.SessionID, bool, error) {
	id, found, err := current.Get(ctx)
	if err != nil {
		return model.SessionID{}, false, fmt.Errorf("%w; run 'arcave session unset' or 'arcave session set <session>'", err)
	}
	zerolog.Ctx(ctx).Debug().Bool("found", found).Str("session", id.String()).Msg("current-session pointer read")
	return id, found, nil
}

// logResolved records which session a command works in and what named it: a ref, the pointer
// or the default.
func logResolved(ctx context.Context, session model.Session, by, ref string) {
	zerolog.Ctx(ctx).Debug().Str("session", session.ID.String()).Str("name", session.Name).
		Str("by", by).Str("ref", ref).Msg("session resolved")
}
