package core

import (
	"context"
	"fmt"

	"github.com/greenmaskio/gm-tools/internal/config"
)

// configWriter persists the project config. Only Init uses it — the read
// commands take a resolved config.Config by value (from cmd/gm resolve) and
// never touch the store.
type configWriter interface {
	Exists() (bool, error)
	Save(config.Config) error
	Path() string
}

// InitService creates a project's GM metadata. It is separate from Service
// because it shares only the compose inspector with the savepoint workflows and
// needs neither storage, nor a snapshot backend, nor the Docker engine — so
// `gm init` runs without ever reaching for a daemon.
type InitService struct {
	projectRoot      string
	configWriter     configWriter
	composeInspector composeInspector
}

// NewInitService wires an InitService for the project rooted at projectRoot.
func NewInitService(projectRoot string, cw configWriter, ci composeInspector) *InitService {
	return &InitService{projectRoot: projectRoot, configWriter: cw, composeInspector: ci}
}

// Init initialises GM metadata for the project: it verifies the compose project
// loads, then writes cfg to .gm/config.yaml. It returns the resolved project
// name and the path of the written config file. An already-initialised project
// is an error unless override is set.
//
// cfg is the value Init persists, verbatim — it takes no request type of its
// own. An InitRequest mirroring a subset of the config could only be a lossy
// copy of it: every section it forgot to carry (docker, snapshot) would be
// silently dropped from the file a `gm init --override` rewrote. It also has
// nothing to create: the savepoint store is a `storage` section here and nothing
// more, brought into existence by storages.New at the first savepoint, so Init
// never names a directory and never opens one.
//
// override is an argument rather than a config field because it is transient:
// it steers this run and is never persisted.
func (s *InitService) Init(ctx context.Context, cfg config.Config, override bool) (resolvedName, path string, err error) {
	exists, err := s.configWriter.Exists()
	if err != nil {
		return "", "", fmt.Errorf("check existing config: %w", err)
	}
	if exists && !override {
		return "", "", fmt.Errorf("project already initialized: %s exists (use --override to overwrite)", s.configWriter.Path())
	}

	// ApplyDefaults stamps the schema version, the computed project name and the
	// union tags, so the file init writes is a complete v1 config. cmd/gm has
	// already run it (config.Unmarshal does), but it is idempotent, and an MCP
	// caller may hand over a Config it assembled by hand.
	cfg.ApplyDefaults(s.projectRoot)

	// Read and verify the compose project before writing anything.
	if _, err := s.composeInspector.Inspect(ctx, cfg.Project.ComposeFiles...); err != nil {
		return "", "", fmt.Errorf("verifying compose project: %w", err)
	}

	if err := s.configWriter.Save(cfg); err != nil {
		return "", "", fmt.Errorf("saving config.yaml file: %w", err)
	}
	return cfg.Project.Name, s.configWriter.Path(), nil
}
