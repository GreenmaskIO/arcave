// Package core holds GM's business logic. It orchestrates the repositories
// (config, savepoint), the snapshot backends (service/snapshot) and the clients
// (compose, docker) to create savepoints, roll back, and initialise projects.
// Each method declares the repository, backend and client behaviour it needs via
// the private interfaces below; the concrete types satisfy them implicitly.
//
// It is the only consumer-facing service package: service/snapshot sits beneath
// it as an implementation of the snapshotBackend seam declared here.
package core

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// savepointRepository reads and writes savepoint records, one per savepoint. It
// lives in the same store as the savepoints it records, so its methods take a
// context: reaching it may cross a network.
//
// Save is the commit and Delete is the uncommit, so the *order* in which the
// service calls them relative to the snapshot backend is a correctness property,
// not a style choice — see snapshotWhileStopped and applyRemoval.
type savepointRepository interface {
	List(ctx context.Context) ([]model.SavepointRecord, error)
	Save(ctx context.Context, record model.SavepointRecord) error
	Delete(ctx context.Context, id string) error
}

// projectLocker serialises the workflows that mutate the project. It is bound to
// its project at construction, so no path crosses this seam — the service knows
// only that a workflow can claim exclusive use of the project and must give it
// back.
//
// It lives here, and not in an adapter, because "a savepoint is never created
// while a rollback is running" is a property of the workflow, not of the CLI that
// happens to invoke it. `gm mcp` calls these methods directly and gets the same
// guarantee for free.
//
// Acquire fails immediately with model.ErrProjectLocked when the project is
// already claimed, rather than blocking: a user who typed a second command wants
// to be told, not queued. release returns an error because a lock backed by a
// network lease can fail to be relinquished.
type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// composeInspector reads the compose project's *declared* state: its name and
// the mounts GM snapshots. It only parses files, so it reaches no daemon — which
// is why InitService can depend on it alone.
type composeInspector interface {
	Inspect(ctx context.Context, files ...string) (model.Inspection, error)
}

// composeRunner drives the *running* stack: it stops the services before GM
// touches a mount and starts them after. It is stateless — the compose files to
// act on are passed per call — so a single runner is injected at the DI root and
// shared across invocations.
type composeRunner interface {
	Stop(ctx context.Context, files ...string) error
	Up(ctx context.Context, waitTimeoutSec int, files ...string) error
}

// snapshotBackend stores and restores a whole savepoint's volume data as a unit.
// Every method is savepoint-scoped, because that is how every caller uses it: the
// backend owns the per-mount fan-out (the concurrency bound, the Docker I/O),
// which is why the service never writes an errgroup over volumes itself. It holds
// the Docker engine and the Storager of the store it owns and never speaks of
// paths — the archive key is the backend's own, derived from the savepoint id
// and each mount, identically on both sides.
//
// A backend is bound to one store at construction, so the service holds two of
// them — one over the savepoint directory, one over the safety-backup directory.
type snapshotBackend interface {
	// Snapshot captures every mount into the savepoint's directory, returning the
	// bytes stored per mount in the same order as mounts.
	Snapshot(ctx context.Context, savepointID, composeProject string, mounts []model.Mount) (sizes []int64, err error)
	// Restore streams every mount back into its Docker mount.
	Restore(ctx context.Context, savepointID, composeProject string, mounts []model.Mount) error
	// Missing reports the mounts whose archive is absent (pre-flight).
	Missing(ctx context.Context, savepointID string, mounts []model.Mount) ([]model.Mount, error)
	// Remove drops a whole savepoint directory — a savepoint's, or a safety
	// backup's. It must succeed on a partially written directory, whose archives
	// the caller never recorded. Being savepoint-scoped is what lets it clean up a
	// snapshot that failed mid-fan-out; the id names which one is dropped.
	Remove(ctx context.Context, savepointID string) error
}

// Service orchestrates GM's workflows. It is constructed once at the DI root
// from the repositories and clients it depends on.
type Service struct {
	savepointRepo    savepointRepository
	projectLocker    projectLocker
	composeInspector composeInspector
	composeRunner    composeRunner
	// The two backends differ only in the store each is bound to, and both expose
	// the same methods — so rollback.go calls Restore on each within twenty lines
	// of the other and the compiler cannot tell them apart. The names must, hence
	// no bare "backend": savepointBackend writes the project's savepoints (under
	// the configured savepoint dir), safetyBackupBackend writes the pre-rollback
	// safety backups (under .gm/tmp) that a failed rollback replays.
	savepointBackend    snapshotBackend
	safetyBackupBackend snapshotBackend
	// now is the clock, a func rather than an interface: for a single method that
	// is the idiomatic Go seam. Production always wants time.Now, so New sets it;
	// a same-package test overrides the field with a fixed clock.
	now func() time.Time
}

// New wires a Service. savepointBackend writes savepoints, safetyBackupBackend
// the safety backups a failed rollback replays. The service holds no Storager of
// its own: every byte it moves goes through one of the two backends.
func New(
	savepointRepo savepointRepository,
	projectLocker projectLocker,
	composeInspector composeInspector,
	composeRunner composeRunner,
	savepointBackend snapshotBackend,
	safetyBackupBackend snapshotBackend,
) *Service {
	return &Service{
		savepointRepo:       savepointRepo,
		projectLocker:       projectLocker,
		composeInspector:    composeInspector,
		composeRunner:       composeRunner,
		savepointBackend:    savepointBackend,
		safetyBackupBackend: safetyBackupBackend,
		now:                 time.Now,
	}
}

// lockProject claims the project for a mutating workflow and returns the
// function that gives it back, to be deferred by the caller.
//
// Releasing is best-effort and cannot change the workflow's outcome — the work is
// already done by the time it runs — so the returned func swallows its error into
// the log rather than handing the caller a second error to reconcile with the
// one it is already returning.
func (s *Service) lockProject(ctx context.Context) (unlock func(), err error) {
	release, err := s.projectLocker.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return func() {
		if err := release(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("release project lock")
		}
	}, nil
}

// savepointsOf projects savepoint records to their savepoint metadata.
func savepointsOf(records []model.SavepointRecord) []model.Savepoint {
	out := make([]model.Savepoint, 0, len(records))
	for _, r := range records {
		out = append(out, r.Savepoint)
	}
	return out
}
