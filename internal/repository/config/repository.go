// Package config persists arcave's per-project configuration to .arcave/config.yaml. The
// configuration value type and its default helpers live in internal/config;
// reading is handled by cmd/arcave (viper). This package is the sole
// writer.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/greenmaskio/arcave/internal/config"
)

// Repository writes the config for the project rooted at projectRoot.
type Repository struct {
	projectRoot string
}

// New returns a config repository for the project rooted at projectRoot.
func New(projectRoot string) *Repository {
	return &Repository{projectRoot: projectRoot}
}

// Path returns the absolute config file path.
func (r *Repository) Path() string {
	return config.Path(r.projectRoot)
}

// Exists reports whether the config file is already present.
func (r *Repository) Exists() (bool, error) {
	_, err := os.Stat(r.Path())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// Save writes cfg to .arcave/config.yaml, creating the .arcave directory if needed. It
// takes cfg by value and does not mutate it.
func (r *Repository) Save(cfg config.Config) error {
	path := r.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
