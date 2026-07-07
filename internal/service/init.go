package service

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/repository/config"
	"github.com/greenmaskio/gm-tools/internal/utils"
)

// InitRequest holds the settings init writes to .gm/config.yaml. Empty fields
// take their defaults (project name from the directory, savepoint dir under
// .gm, and the default log level/format).
type InitRequest struct {
	Name         string
	ComposeFiles []string
	SavepointDir string
	LogLevel     string
	LogFormat    string
	// Override overwrites an existing config instead of failing.
	Override bool
}

// Init initialises GM metadata for the project: it verifies the compose project
// loads, then writes .gm/config.yaml. It returns the resolved project name and
// the path of the written config file. An already-initialised project is an
// error unless req.Override is set.
func (s *Service) Init(ctx context.Context, req InitRequest) (resolvedName, path string, err error) {
	exists, err := s.cfg.Exists()
	if err != nil {
		return "", "", fmt.Errorf("check existing config: %w", err)
	}
	if exists && !req.Override {
		return "", "", fmt.Errorf("project already initialized: %s exists (use --override to overwrite)", s.cfg.Path())
	}

	// Apply defaults that depend on the resolved project name.
	name := req.Name
	if name == "" {
		name = filepath.Base(s.projectRoot)
	}
	savepointDir := req.SavepointDir
	if savepointDir == "" {
		savepointDir = filepath.Join(".gm", "savepoints", name)
	}
	logLevel := req.LogLevel
	if logLevel == "" {
		logLevel = utils.DefaultLogLevel
	}
	logFormat := req.LogFormat
	if logFormat == "" {
		logFormat = utils.DefaultLogFormat
	}

	// Read and verify the compose project before writing anything.
	if _, err := s.compose.Inspect(ctx, req.ComposeFiles...); err != nil {
		return "", "", fmt.Errorf("verifying compose project: %w", err)
	}

	cfg := &config.Config{
		Name:         name,
		ComposeFiles: req.ComposeFiles,
		SavepointDir: savepointDir,
		Log:          config.Log{Level: logLevel, Format: logFormat},
	}
	if err := s.cfg.Save(cfg); err != nil {
		return "", "", fmt.Errorf("saving config.yaml file: %w", err)
	}
	return name, s.cfg.Path(), nil
}

// ConfigLog returns the project's persisted log level and format. ok is false
// when the config cannot be loaded (e.g. before init), so callers fall back to
// flag values or defaults.
func (s *Service) ConfigLog() (level, format string, ok bool) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return "", "", false
	}
	return cfg.Log.Level, cfg.Log.Format, true
}
