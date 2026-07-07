package service

import (
	"context"
	"fmt"

	"github.com/greenmaskio/gm-tools/internal/config"
)

// Init initialises GM metadata for the project: it verifies the compose project
// loads, then writes .gm/config.yaml. cfg is the config resolved from init's
// flags and defaults (appconfig.Resolve); Init fills the name-dependent computed
// defaults and persists it. It returns the resolved project name and the path of
// the written config file. An already-initialised project is an error unless
// override is set.
func (s *Service) Init(ctx context.Context, cfg config.Config, override bool) (resolvedName, path string, err error) {
	exists, err := s.cfg.Exists()
	if err != nil {
		return "", "", fmt.Errorf("check existing config: %w", err)
	}
	if exists && !override {
		return "", "", fmt.Errorf("project already initialized: %s exists (use --override to overwrite)", s.cfg.Path())
	}

	cfg.ApplyDefaults(s.projectRoot)

	// Read and verify the compose project before writing anything.
	if _, err := s.compose.Inspect(ctx, cfg.ComposeFiles...); err != nil {
		return "", "", fmt.Errorf("verifying compose project: %w", err)
	}

	if err := s.cfg.Save(cfg); err != nil {
		return "", "", fmt.Errorf("saving config.yaml file: %w", err)
	}
	return cfg.Name, s.cfg.Path(), nil
}
