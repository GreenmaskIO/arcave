// Package service holds GM's business logic. It orchestrates the repositories
// (config, manifest, snapshot) and clients (compose, docker) to create
// savepoints, roll back, and initialise projects. Each service method declares
// the repository and client behaviour it needs via the private interfaces
// below; the concrete types satisfy them implicitly.
package service

import (
	"context"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/config"
	"github.com/greenmaskio/gm-tools/internal/repository/manifest"
)

// configStore reads and writes the project config.
type configStore interface {
	Load() (*config.Config, error)
	Save(*config.Config) error
	Exists() (bool, error)
	Path() string
}

// manifestStore reads and writes the savepoint index.
type manifestStore interface {
	Load() (*manifest.Manifest, error)
	Save(*manifest.Manifest) error
}

// composeInspector resolves the compose project's name and the mounts GM
// snapshots.
type composeInspector interface {
	Inspect(ctx context.Context, files ...string) (compose.Inspection, error)
}

// Service orchestrates GM's workflows. It is constructed once at the DI root
// with the project root and the repositories/clients it depends on.
type Service struct {
	projectRoot string
	cfg         configStore
	mf          manifestStore
	compose     composeInspector
}

// New wires a Service for the project rooted at projectRoot.
func New(projectRoot string, cfg configStore, mf manifestStore, comp composeInspector) *Service {
	return &Service{projectRoot: projectRoot, cfg: cfg, mf: mf, compose: comp}
}

// abs resolves p against the project root unless it is already absolute.
func (s *Service) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.projectRoot, p)
}

// snapshotsDir returns the directory holding savepoint data, relative to the
// project root unless the config overrides it with an absolute path.
func snapshotsDir(cfg *config.Config) string {
	if cfg.SavepointDir != "" {
		return cfg.SavepointDir
	}
	return filepath.Join(".gm", "snapshots")
}

// savepointsOf projects manifest records to their savepoint metadata.
func savepointsOf(records []manifest.Record) []model.Savepoint {
	out := make([]model.Savepoint, 0, len(records))
	for _, r := range records {
		out = append(out, r.Savepoint)
	}
	return out
}
