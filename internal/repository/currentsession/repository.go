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

// Package currentsession persists the host-local current-session pointer, .arcave/current_session.
package currentsession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

const pointerFile = "current_session"

// Repository is the pointer's CRUD: at most one current session per project directory.
type Repository struct {
	path string
}

func New(projectRoot string) *Repository {
	return &Repository{path: filepath.Join(projectRoot, model.StateDir, pointerFile)}
}

// Get reads the id the pointer holds; found is false when none is set.
func (r *Repository) Get(_ context.Context) (id model.SessionID, found bool, err error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return model.SessionID{}, false, nil
	}
	if err != nil {
		return model.SessionID{}, false, fmt.Errorf("reading %s: %w", r.path, err)
	}
	id, err = model.ParseSessionID(strings.TrimSpace(string(data)))
	if err != nil {
		return model.SessionID{}, false, fmt.Errorf("decoding %s: %w", r.path, err)
	}
	return id, true, nil
}

// Put points at id, replacing any earlier pointer. It renames a written file into place, so a
// reader never sees a partial one.
func (r *Repository) Put(ctx context.Context, id model.SessionID) (err error) {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, pointerFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating the temporary pointer in %s: %w", dir, err)
	}
	defer func() {
		if err == nil {
			return
		}
		if closeErr := tmp.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			zerolog.Ctx(ctx).Warn().Err(closeErr).Str("path", tmp.Name()).Msg("closing the temporary pointer")
		}
		if removeErr := os.Remove(tmp.Name()); removeErr != nil {
			zerolog.Ctx(ctx).Warn().Err(removeErr).Str("path", tmp.Name()).Msg("removing the temporary pointer")
		}
	}()
	if _, err := tmp.WriteString(id.String() + "\n"); err != nil {
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), r.path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp.Name(), r.path, err)
	}
	return nil
}

// Clear removes the pointer; an absent one is already cleared.
func (r *Repository) Clear(_ context.Context) error {
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", r.path, err)
	}
	return nil
}
