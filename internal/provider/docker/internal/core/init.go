package core

import (
	"context"
	"fmt"

	"github.com/greenmaskio/arcave/internal/config"
)

// configWriter persists the project config. Only Init uses it: every other
// command takes a resolved config.Config by value.
type configWriter interface {
	Exists() (bool, error)
	Save(config.Config) error
	Path() string
}

// InitService creates a project's arcave metadata. It shares only the compose
// inspector with the savepoint workflows and needs neither storage, a snapshot
// backend nor the Docker engine, so `arcave init` never reaches for a arcaved.
type InitService struct {
	projectRoot         string
	configWriter        configWriter
	composeIntrospector composeIntrospector
}

// NewInitService wires an InitService for the project rooted at projectRoot.
func NewInitService(projectRoot string, cw configWriter, ci composeIntrospector) *InitService {
	return &InitService{projectRoot: projectRoot, configWriter: cw, composeIntrospector: ci}
}

// Init verifies the compose project loads, then writes cfg verbatim to
// .arcave/config.yaml. An initialised project is an error unless override is set.
func (s *InitService) Init(
	ctx context.Context,
	cfg config.Config,
	composeFiles []string,
	override bool,
) (resolvedName, path string, err error) {
	exists, err := s.configWriter.Exists()
	if err != nil {
		return "", "", fmt.Errorf("check existing config: %w", err)
	}
	if exists && !override {
		return "", "", fmt.Errorf("project already initialized: %s exists (use --override to overwrite)", s.configWriter.Path())
	}

	// Stamps the schema version, project name and union tags, so the file is a
	// complete v1 config. Idempotent: cmd/arcave has run it already, but an MCP
	// caller may hand over a Config it assembled by hand.
	cfg.ApplyDefaults(s.projectRoot)

	// Read and verify the compose project before writing anything.
	if _, err := s.composeIntrospector.Introspect(ctx, composeFiles...); err != nil {
		return "", "", fmt.Errorf("verifying compose project: %w", err)
	}

	if err := s.configWriter.Save(cfg); err != nil {
		return "", "", fmt.Errorf("saving config.yaml file: %w", err)
	}
	return cfg.Project.Name, s.configWriter.Path(), nil
}
