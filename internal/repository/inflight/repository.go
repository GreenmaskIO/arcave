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

// Put writes the marker, replacing any standing one.
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
