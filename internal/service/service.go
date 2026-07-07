// Package service holds GM's business logic. It orchestrates the repositories
// (config, manifest, snapshot) and clients (compose, docker) to create
// savepoints, roll back, and initialise projects. Each service method declares
// the repository and client behaviour it needs via the private interfaces
// below; the concrete types satisfy them implicitly.
package service

import (
	"context"
	"io"
	"path/filepath"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/client/docker"
	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/utils/ioutils"
	"github.com/greenmaskio/gm-tools/internal/volume"
)

// configWriter persists the project config. It is used only by Init — the read
// methods take a resolved config.Config by value (from appconfig.Resolve) and
// never touch the store.
type configWriter interface {
	Exists() (bool, error)
	Save(config.Config) error
	Path() string
}

// manifestStore reads and writes the savepoint index.
type manifestStore interface {
	Load() ([]model.SavepointRecord, error)
	Save([]model.SavepointRecord) error
}

// composeInspector resolves the compose project's name and the mounts GM
// snapshots.
type composeInspector interface {
	Inspect(ctx context.Context, files ...string) (compose.Inspection, error)
}

// composeRunner stops and starts the project's services (docker compose
// stop/up). Rollback and CreateSavepoint construct one per invocation from the
// resolved compose files, so it is supplied by a factory rather than injected
// directly.
type composeRunner interface {
	Stop(ctx context.Context) error
	Up(ctx context.Context, waitTimeoutSec int) error
}

// volumeEngine streams service mount data in and out of containers through the
// Docker Engine API. OpenMount yields a tar stream of a mount's contents;
// WriteMount wipes a mount and extracts a tar stream back into it. Both are the
// low-level halves volume.Copy wires to a store endpoint.
//
// Close tears down the Docker daemon connection (the wrapped *client.Client). It
// is a per-command resource, not a per-copy one: called once via defer in
// CreateSavepoint/Rollback after every mount is copied. Closing the per-copy
// source stream is volume.Copy's job, not this method's.
type volumeEngine interface {
	OpenMount(ctx context.Context, project, service, target string) (io.ReadCloser, error)
	WriteMount(ctx context.Context, project, service, target string, r io.Reader) error
	Close() error
}

// Service orchestrates GM's workflows. It is constructed once at the DI root
// with the project root and the repositories/clients it depends on.
type Service struct {
	projectRoot string
	cfg         configWriter
	mf          manifestStore
	compose     composeInspector
	// store is the single storage backend the snapshot and backup stores are
	// re-rooted from (via SubStorage); the service sees it only as model.Storager.
	store model.Storager
	// newCompose and newEngine construct the per-invocation docker clients.
	// They default to the real docker types; tests replace them with factories
	// returning doubles so orchestration can be exercised without a daemon.
	newCompose func(files ...string) composeRunner
	newEngine  func() (volumeEngine, error)
}

// New wires a Service for the project rooted at projectRoot.
func New(projectRoot string, cfg configWriter, mf manifestStore, comp composeInspector, store model.Storager) *Service {
	return &Service{
		projectRoot: projectRoot,
		cfg:         cfg,
		mf:          mf,
		compose:     comp,
		store:       store,
		newCompose:  func(files ...string) composeRunner { return docker.NewComposeClient(files...) },
		newEngine:   func() (volumeEngine, error) { return docker.NewEngine() },
	}
}

// subStorage re-roots the service's Storager at dir. dir may be relative to the
// project root (.gm/snapshots, .gm/tmp) or an absolute override (a configured
// SavepointDir); SubStorage's relative flag is set from that distinction so an
// absolute path is used verbatim rather than joined onto the project root.
func (s *Service) subStorage(dir string) model.Storager {
	return s.store.SubStorage(dir, !filepath.IsAbs(dir))
}

// engineReader adapts (engine, project, service, target) to a volume.Reader that
// streams the mount's contents out of its container. It lives here rather than in
// the docker package to avoid a docker→service import cycle over volume.Reader.
type engineReader struct {
	eng     volumeEngine
	project string
	service string
	target  string
}

// newEngineReader builds a volume.Reader over the given mount.
func newEngineReader(eng volumeEngine, project, service, target string) engineReader {
	return engineReader{eng: eng, project: project, service: service, target: target}
}

// engineWriter adapts the same tuple to a volume.Writer that wipes the mount and
// extracts a tar stream back into its container.
type engineWriter struct {
	eng     volumeEngine
	project string
	service string
	target  string
}

// newEngineWriter builds a volume.Writer over the given mount.
func newEngineWriter(eng volumeEngine, project, service, target string) engineWriter {
	return engineWriter{eng: eng, project: project, service: service, target: target}
}

var (
	_ volume.Reader = engineReader{}
	_ volume.Writer = engineWriter{}
)

func (r engineReader) Open(ctx context.Context) (io.ReadCloser, error) {
	return r.eng.OpenMount(ctx, r.project, r.service, r.target)
}

func (r engineReader) DebugInfo() map[string]any {
	return map[string]any{"service": r.service, "target": r.target}
}

func (w engineWriter) Write(ctx context.Context, r io.Reader) (int64, error) {
	// CopyToContainer reports no byte count, so a counting reader tallies what we
	// streamed into the container as the stored-bytes figure.
	counted := ioutils.NewReader(io.NopCloser(r))
	if err := w.eng.WriteMount(ctx, w.project, w.service, w.target, counted); err != nil {
		return 0, err
	}
	return counted.GetCount(), nil
}

func (w engineWriter) DebugInfo() map[string]any {
	return map[string]any{"service": w.service, "target": w.target}
}

// snapshotsDir returns the directory holding savepoint data, relative to the
// project root unless the config overrides it with an absolute path.
func snapshotsDir(cfg config.Config) string {
	if cfg.SavepointDir != "" {
		return cfg.SavepointDir
	}
	return filepath.Join(".gm", "snapshots")
}

// savepointsOf projects savepoint records to their savepoint metadata.
func savepointsOf(records []model.SavepointRecord) []model.Savepoint {
	out := make([]model.Savepoint, 0, len(records))
	for _, r := range records {
		out = append(out, r.Savepoint)
	}
	return out
}
