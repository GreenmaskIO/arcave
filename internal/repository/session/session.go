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
	"slices"
	"strings"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/storefactory"
	"github.com/greenmaskio/arcave/internal/utils"
)

const (
	namespaceSessions = "sessions"
	recordName        = "session.json"
	// copyConcurrency bounds the objects CopyAll copies at once.
	copyConcurrency = 5
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
	ids, err := r.sessionIDs(ctx)
	if err != nil {
		return nil, err
	}
	records := []model.Session{model.DefaultSessionRecord}
	for _, id := range ids {
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

// ListUncommittedSessions returns the ids of the session trees without a record: forks still
// copying or never committed, and deletes that were interrupted. The default session is none.
func (r *Repository) ListUncommittedSessions(ctx context.Context) ([]model.SessionID, error) {
	ids, err := r.sessionIDs(ctx)
	if err != nil {
		return nil, err
	}
	var uncommitted []model.SessionID
	for _, id := range ids {
		if id == model.DefaultSession {
			continue
		}
		exists, err := r.storage.Exists(ctx, recordKey(id))
		if err != nil {
			return nil, fmt.Errorf("stat session record %s: %w", id, err)
		}
		if !exists {
			uncommitted = append(uncommitted, id)
		}
	}
	return uncommitted, nil
}

// SessionSize sums the size of every object under sessions/<id> — exactly what DeleteSession
// frees, orphans and uncollected blobs included. An absent session measures 0.
func (r *Repository) SessionSize(ctx context.Context, id model.SessionID) (int64, error) {
	sessionStore, err := storefactory.Session(r.storage, id)
	if err != nil {
		return 0, fmt.Errorf("measure session %s: %w", id, err)
	}
	objects, err := sessionStore.List(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("measure session %s: %w", id, err)
	}
	size, _ := utils.MeasureObjects(objects)
	return size, nil
}

// CopySession copies sessions/<from> into sessions/<to> but the record and the root keys in
// except, and returns the bytes written; it does not check that to is empty. Each top-level
// directory goes in one CopyAll, server-side where the store can.
func (r *Repository) CopySession(ctx context.Context, from, to model.SessionID, except ...string) (int64, error) {
	if from == to {
		return 0, fmt.Errorf("%w: copy session %s onto itself", model.ErrSessionTreeOccupied, from)
	}
	if err := validateID(to); err != nil {
		return 0, fmt.Errorf("copy session: %w", err)
	}
	src, err := storefactory.Session(r.storage, from)
	if err != nil {
		return 0, fmt.Errorf("open session %s: %w", from, err)
	}
	files, dirs, err := src.ListDir(ctx)
	if err != nil {
		return 0, fmt.Errorf("list session %s: %w", from, err)
	}

	copiedBytes, err := copySessionTree(ctx, r.storage, from, to, rootKeys(files, except), dirNames(dirs))
	if err != nil {
		return copiedBytes, fmt.Errorf("copy session %s to %s: %w", from, to, err)
	}
	return copiedBytes, nil
}

// IsSessionTreeEmpty reports whether sessions/<id> holds no object at all, a heartbeat included.
func (r *Repository) IsSessionTreeEmpty(ctx context.Context, id model.SessionID) (bool, error) {
	store, err := storefactory.Session(r.storage, id)
	if err != nil {
		return false, fmt.Errorf("open session %s: %w", id, err)
	}
	objects, err := store.List(ctx, "")
	if err != nil {
		return false, fmt.Errorf("list session %s: %w", id, err)
	}
	return len(objects) == 0, nil
}

// DeleteSession removes a session's record first, then everything under sessions/<id>; for
// model.DefaultSession, which has no record, it empties the tree. Deleting a session already gone is a no-op.
func (r *Repository) DeleteSession(ctx context.Context, id model.SessionID) error {
	if err := id.Validate(); err != nil {
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

// sessionIDs returns the ids of the directories under sessions/, skipping any that is not one.
func (r *Repository) sessionIDs(ctx context.Context) ([]model.SessionID, error) {
	sessionsStore, err := r.storage.SubStorage(namespaceSessions, true)
	if err != nil {
		return nil, fmt.Errorf("re-root storage at %s: %w", namespaceSessions, err)
	}
	_, dirs, err := sessionsStore.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session store: %w", err)
	}
	ids := make([]model.SessionID, 0, len(dirs))
	for _, dir := range dirs {
		id, err := model.ParseSessionID(dir.Dirname())
		if err != nil {
			log.Ctx(ctx).Debug().Str("entry", dir.Dirname()).Msg("ignoring directory that is not a session")
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
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

	obj, err := r.storage.GetObject(ctx, recordKey(id))
	if err != nil {
		return model.Session{}, false, fmt.Errorf("read session record %s: %w", id, err)
	}
	defer func() {
		if err := obj.Close(); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("session", id.String()).Msg("close session record")
		}
	}()

	var record model.Session
	if err := json.NewDecoder(obj).Decode(&record); err != nil {
		return model.Session{}, false, fmt.Errorf("parse session record %s: %w", id, err)
	}
	return record, true, nil
}

// sessionKey is the session's directory relative to the arcave project store.
func sessionKey(id model.SessionID) string {
	return path.Join(namespaceSessions, id.String())
}

// recordKey is the record's key relative to the arcave project store.
func recordKey(id model.SessionID) string {
	return path.Join(sessionKey(id), recordName)
}

// rootKeys are a session's top-level files but the record, the keys in except and folder markers.
func rootKeys(files, except []string) []string {
	var keys []string
	for _, name := range files {
		// A name ending in "/" is an object store's folder marker, which no store can copy.
		if name == "" || name == recordName || slices.Contains(except, name) || strings.HasSuffix(name, "/") {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// dirNames are the names of a session's top-level directories.
func dirNames(dirs []storages.Storager) []string {
	names := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names = append(names, dir.Dirname())
	}
	return names
}

// copySessionTree copies every top-level directory whole and every root key to the same key
// under sessions/<to>, and returns the bytes written. A cancelled ctx starts no further copy.
func copySessionTree(
	ctx context.Context, st storages.Storager, from, to model.SessionID, keys, dirs []string,
) (int64, error) {
	var written int64
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		stat, err := copyPrefix(ctx, st, path.Join(sessionKey(from), dir), path.Join(sessionKey(to), dir))
		written += stat.Bytes
		if err != nil {
			return written, fmt.Errorf("copy %s: %w", dir, err)
		}
		log.Ctx(ctx).Info().Str("session", to.String()).Str("tree", dir).Int64("bytes", stat.Bytes).
			Int("objects", stat.Objects).Bool("streamed", stat.Streamed).Msg("copied")
	}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		stat, err := st.Copy(ctx, path.Join(sessionKey(from), key), path.Join(sessionKey(to), key),
			storages.WithClientSideCopyFallback())
		written += stat.Bytes
		if err != nil {
			return written, fmt.Errorf("copy %s: %w", key, err)
		}
	}
	return written, nil
}

// copyPrefix copies everything under src to dst. A prefix holding no object, which CopyAll
// refuses as not found, copies nothing.
func copyPrefix(ctx context.Context, st storages.Storager, src, dst string) (storages.CopyStat, error) {
	stat, err := st.CopyAll(ctx, src, dst,
		storages.WithCopyAllObjectConcurrency(copyConcurrency), storages.WithCopyAllClientSideFallback())
	if err == nil || stat.Objects > 0 || !errors.Is(err, storages.ErrFileNotFound) {
		return stat, err
	}
	// CopyAll's not-found is also an object gone mid-copy: only an empty listing tells them apart.
	objects, listErr := st.List(ctx, src)
	if listErr != nil {
		log.Ctx(ctx).Warn().Err(listErr).Str("prefix", src).Msg("listing a prefix CopyAll found empty")
		return stat, err
	}
	if slices.ContainsFunc(objects, func(o storages.ObjectStat) bool { return !strings.HasSuffix(o.Name, "/") }) {
		return stat, err
	}
	return storages.CopyStat{}, nil
}
