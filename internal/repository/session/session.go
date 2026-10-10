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

// Package session reads and writes session records at sessions/<id>/session.json.
// The record is the session's commit marker; model.DefaultSession has none.
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

const (
	sessionsNamespace = "sessions"
	recordName        = "session.json"
)

// errDefaultSessionRecord refuses a record for the session that exists implicitly.
var errDefaultSessionRecord = errors.New("the default session has no record")

// Repository reads and writes session records inside an arcave project store.
type Repository struct {
	storage storages.Storager
}

// New returns a session repository over an arcave project store.
func New(store storages.Storager) *Repository {
	return &Repository{storage: store}
}

// SaveSession writes a session's record; the caller writes the session's data first.
func (r *Repository) SaveSession(ctx context.Context, session model.Session) error {
	if err := validateID(session.ID); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session %s: %w", session.ID, err)
	}
	data = append(data, '\n')

	if err := r.storage.PutObject(ctx, recordKey(session.ID), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write session record %s: %w", session.ID, err)
	}
	return nil
}

// LoadSession reads one session's record; found is false when it has none.
// DefaultSession is always found, as model.DefaultSessionRecord.
func (r *Repository) LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error) {
	if id == model.DefaultSession {
		return model.DefaultSessionRecord, true, nil
	}
	if err := id.Validate(); err != nil {
		return model.Session{}, false, fmt.Errorf("load session: %w", err)
	}
	return r.load(ctx, id)
}

// ListSessions returns model.DefaultSessionRecord, then every committed record. A session
// directory with no record is incomplete and skipped; a record that will not decode is an error.
func (r *Repository) ListSessions(ctx context.Context) ([]model.Session, error) {
	sessionsStore, err := r.storage.SubStorage(sessionsNamespace, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", sessionsNamespace, err)
	}
	_, dirs, err := sessionsStore.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session store: %w", err)
	}

	records := []model.Session{model.DefaultSessionRecord}
	for _, dir := range dirs {
		id, err := model.ParseSessionID(dir.Dirname())
		if err != nil {
			log.Ctx(ctx).Debug().Str("entry", dir.Dirname()).Msg("ignoring directory that holds no session record")
			continue
		}
		record, found, err := r.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			log.Ctx(ctx).Debug().Str("session", id.String()).Msg("skipping incomplete session: no record")
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

// ListSessionSavepoints returns every committed savepoint of a session, grouped by compose
// project and ordered by its name. A compose project holding none is left out.
func (r *Repository) ListSessionSavepoints(ctx context.Context, id model.SessionID) ([]model.ComposeProjectSavepoints, error) {
	sessionStore, err := storefactory.Session(r.storage, id)
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
		savepoints, err := sprepo.New(composeProjectStore).ListSavepoints(ctx)
		if err != nil {
			return nil, fmt.Errorf("list savepoints of compose project %s: %w", name, err)
		}
		if len(savepoints) == 0 {
			continue
		}
		groups = append(groups, model.ComposeProjectSavepoints{ComposeProject: name, Savepoints: savepoints})
	}
	return groups, nil
}

// DeleteSession removes a session's record first, then everything under sessions/<id>.
// Deleting a session that is already gone is a no-op.
func (r *Repository) DeleteSession(ctx context.Context, id model.SessionID) error {
	if err := validateID(id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	exists, err := r.storage.Exists(ctx, recordKey(id))
	if err != nil {
		return fmt.Errorf("stat session record %s: %w", id, err)
	}
	if exists {
		if err := r.storage.Delete(ctx, recordKey(id)); err != nil {
			return fmt.Errorf("delete session record %s: %w", id, err)
		}
	}
	if err := r.storage.DeleteAll(ctx, sessionKey(id)); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}

// validateID accepts an id that may carry a record: a valid one other than the default.
func validateID(id model.SessionID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if id == model.DefaultSession {
		return errDefaultSessionRecord
	}
	return nil
}

// load reads one session's record; found is false when the session has none.
func (r *Repository) load(ctx context.Context, id model.SessionID) (model.Session, bool, error) {
	exists, err := r.storage.Exists(ctx, recordKey(id))
	if err != nil {
		return model.Session{}, false, fmt.Errorf("stat session record %s: %w", id, err)
	}
	if !exists {
		return model.Session{}, false, nil
	}

	src, err := r.storage.GetObject(ctx, recordKey(id))
	if err != nil {
		return model.Session{}, false, fmt.Errorf("read session record %s: %w", id, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("session", id.String()).Msg("close session record")
		}
	}()

	var record model.Session
	if err := json.NewDecoder(src).Decode(&record); err != nil {
		return model.Session{}, false, fmt.Errorf("parse session record %s: %w", id, err)
	}
	return record, true, nil
}

// sessionKey is the session's directory relative to the arcave project store.
func sessionKey(id model.SessionID) string {
	return path.Join(sessionsNamespace, id.String())
}

// recordKey is the record's key relative to the arcave project store.
func recordKey(id model.SessionID) string {
	return path.Join(sessionKey(id), recordName)
}
