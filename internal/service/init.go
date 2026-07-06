package service

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/repository/config"
)

// Init initialises GM metadata for the project: it verifies the compose project
// loads, then writes .gm/config.yaml. It returns the resolved project name and
// the path of the written config file. An already-initialised project is an
// error.
func (s *Service) Init(ctx context.Context, name string, composeFiles []string, savepointDir string) (resolvedName, path string, err error) {
	exists, err := s.cfg.Exists()
	if err != nil {
		return "", "", fmt.Errorf("check existing config: %w", err)
	}
	if exists {
		return "", "", fmt.Errorf("project already initialized: %s exists", s.cfg.Path())
	}

	// Apply defaults that depend on the resolved project name.
	if name == "" {
		name = filepath.Base(s.projectRoot)
	}
	if savepointDir == "" {
		savepointDir = filepath.Join(".gm", "savepoints", name)
	}

	// Read and verify the compose project before writing anything.
	if _, err := s.compose.Inspect(ctx, composeFiles...); err != nil {
		return "", "", fmt.Errorf("verifying compose project: %w", err)
	}

	cfg := &config.Config{
		Name:         name,
		ComposeFiles: composeFiles,
		SavepointDir: savepointDir,
	}
	if err := s.cfg.Save(cfg); err != nil {
		return "", "", fmt.Errorf("saving config.yaml file: %w", err)
	}
	return name, s.cfg.Path(), nil
}
