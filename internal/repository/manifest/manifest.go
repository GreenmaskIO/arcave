// Package manifest reads and writes the savepoint index at .gm/manifest.json.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/model"
)

const (
	gmDir    = ".gm"
	fileName = "manifest.json"
)

// file is the on-disk shape of .gm/manifest.json. It is private to the
// repository: the {"savepoints": [...]} envelope is a storage detail, so the
// Load/Save contract is expressed purely in terms of the shared model types.
type file struct {
	Savepoints []model.SavepointRecord `json:"savepoints"`
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

// Load reads .gm/manifest.json and returns every savepoint record; a missing
// file yields an empty slice so list/remove work before the first savepoint is
// created.
func (r *Repository) Load() ([]model.SavepointRecord, error) {
	data, err := os.ReadFile(r.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", r.Path(), err)
	}
	return f.Savepoints, nil
}

// Save writes the savepoint records atomically (temp file + rename) so a crash
// mid-write cannot corrupt the savepoint index.
func (r *Repository) Save(records []model.SavepointRecord) error {
	data, err := json.MarshalIndent(file{Savepoints: records}, "", "  ")
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
