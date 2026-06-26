// Package config reads and writes GM's per-project configuration at
// .gm/config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	gmDir    = ".gm"
	fileName = "config.yaml"
)

// Config is the content persisted to .gm/config.yaml by init.
type Config struct {
	Name         string   `yaml:"name"`
	ComposeFiles []string `yaml:"compose_files"`
	SavepointDir string   `yaml:"savepoint_dir"`
}

// Repository reads and writes the config for the project rooted at projectRoot.
type Repository struct {
	projectRoot string
}

// New returns a config repository for the project rooted at projectRoot.
func New(projectRoot string) *Repository {
	return &Repository{projectRoot: projectRoot}
}

// Path returns the absolute config file path.
func (r *Repository) Path() string {
	return filepath.Join(r.projectRoot, gmDir, fileName)
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

// Load reads and parses .gm/config.yaml. A missing file is reported with a
// hint to run 'gm init'.
func (r *Repository) Load() (*Config, error) {
	data, err := os.ReadFile(r.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("project not initialized: %s not found (run 'gm init')", r.Path())
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// Save writes cfg to .gm/config.yaml, creating the .gm directory if needed.
func (r *Repository) Save(cfg *Config) error {
	if err := os.MkdirAll(filepath.Join(r.projectRoot, gmDir), 0o755); err != nil {
		return fmt.Errorf("create %s directory: %w", gmDir, err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(r.Path(), data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
