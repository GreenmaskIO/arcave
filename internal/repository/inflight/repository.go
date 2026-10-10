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

// Package inflight persists the rollback intent marker, .arcave/inflight.json.
// Through os, like repository/config: the marker is host-local state and belongs
// beside the lock, not in a possibly shared savepoint store.
package inflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/greenmaskio/arcave/internal/model"
)

const markerFile = "inflight.json"

// Repository is the marker's CRUD: at most one marker per project.
type Repository struct {
	path string
}

func New(projectRoot string) *Repository {
	return &Repository{path: filepath.Join(projectRoot, ".arcave", markerFile)}
}

// Get reads the marker; found is false when none stands.
func (r *Repository) Get(_ context.Context) (marker model.InflightMarker, found bool, err error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return model.InflightMarker{}, false, nil
	}
	if err != nil {
		return model.InflightMarker{}, false, fmt.Errorf("reading the inflight marker: %w", err)
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return model.InflightMarker{}, false, fmt.Errorf("decoding the inflight marker %s: %w", r.path, err)
	}
	return marker, true, nil
}

// Put writes the marker, replacing any existing one.
func (r *Repository) Put(_ context.Context, marker model.InflightMarker) error {
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the inflight marker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("writing the inflight marker: %w", err)
	}
	if err := os.WriteFile(r.path, data, 0o644); err != nil {
		return fmt.Errorf("writing the inflight marker: %w", err)
	}
	return nil
}

// Clear removes the marker; an absent one is already cleared.
func (r *Repository) Clear(_ context.Context) error {
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the inflight marker: %w", err)
	}
	return nil
}
