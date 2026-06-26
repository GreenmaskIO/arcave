// Package manifest reads and writes the savepoint index at .gm/manifest.json.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/snapshot"
)

const (
	gmDir    = ".gm"
	fileName = "manifest.json"
)

// Record is one savepoint entry persisted in .gm/manifest.json: the savepoint
// metadata plus one VolumeRef per snapshotted mount.
type Record struct {
	model.Savepoint
	Volumes []snapshot.VolumeRef `json:"volumes"`
}

// Manifest is the savepoint index persisted in .gm/manifest.json.
type Manifest struct {
	Savepoints []Record `json:"savepoints"`
}

// Repository reads and writes the manifest for the project rooted at
// projectRoot.
type Repository struct {
	projectRoot string
}

// New returns a manifest repository for the project rooted at projectRoot.
func New(projectRoot string) *Repository {
	return &Repository{projectRoot: projectRoot}
}

// Path returns the absolute manifest file path.
func (r *Repository) Path() string {
	return filepath.Join(r.projectRoot, gmDir, fileName)
}

// Load reads .gm/manifest.json; a missing file yields an empty manifest so
// list/remove work before the first savepoint is created.
func (r *Repository) Load() (*Manifest, error) {
	data, err := os.ReadFile(r.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Manifest{}, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", r.Path(), err)
	}
	return &m, nil
}

// Save writes the manifest atomically (temp file + rename) so a crash mid-write
// cannot corrupt the savepoint index.
func (r *Repository) Save(m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Join(r.projectRoot, gmDir), 0o755); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	tmp := r.Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := os.Rename(tmp, r.Path()); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
