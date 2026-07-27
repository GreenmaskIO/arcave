package corev2

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// maxConcurrentSnapshots caps how many volume snapshots run in parallel.
const maxConcurrentSnapshots = 5

type savepointRepository interface {
	List(ctx context.Context) ([]model.Savepoint, error)
	Save(ctx context.Context, record model.Savepoint) error
	Delete(ctx context.Context, id string) error
}

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

type composeInspector interface {
	Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error)
}

type composeRunner interface {
	Stop(ctx context.Context, files ...string) error
	Up(ctx context.Context, waitTimeout time.Duration, files ...string) error
}

type snapshotBackendRegistry interface {
	New(spec BackendSpec) (VolumeSnapshotBackend, error)
}

// Service orchestrates arcave's savepoint workflows.
type Service struct {
	savepointRepo           savepointRepository
	projectLocker           projectLocker
	composeInspector        composeInspector
	composeRunner           composeRunner
	snapshotBackendRegistry snapshotBackendRegistry
	savepointStore          storages.Storager
	safetyBackupStore       storages.Storager
}

// New wires a Service. The two stores share a type, so swapping them would write
// a savepoint into the scratch tree and compile clean — never name either one a
// bare store.
func New(
	savepointRepo savepointRepository,
	projectLocker projectLocker,
	composeInspector composeInspector,
	composeRunner composeRunner,
	snapshotBackendRegistry snapshotBackendRegistry,
	savepointStore storages.Storager,
	safetyBackupStore storages.Storager,
) *Service {
	return &Service{
		savepointRepo:           savepointRepo,
		projectLocker:           projectLocker,
		composeInspector:        composeInspector,
		composeRunner:           composeRunner,
		snapshotBackendRegistry: snapshotBackendRegistry,
		savepointStore:          savepointStore,
		safetyBackupStore:       safetyBackupStore,
	}
}

// CreateSavepoint mints one backend per volume marked for backup and snapshots
// them in parallel under one savepoint id. Lock, Stop/Up and persisting the
// SavepointRecord are deliberately absent at this step.
func (s *Service) CreateSavepoint(ctx context.Context, req model.CreateSavepointRequest) (model.Savepoint, error) {
	introspectionResult, err := s.composeInspector.Introspect(ctx, req.ComposeFiles...)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("introspect docker compose files: %w", err)
	}
	volumes, skipped, err := FindBackupVolumes(introspectionResult)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("deciding which volumes to back up: %w", err)
	}
	for _, sk := range skipped {
		zerolog.Ctx(ctx).Warn().
			Str("service", sk.Service).
			Str("target", sk.Target).
			Str("raw_type", sk.RawType).
			Str("reason", sk.Reason).
			Msg("mount will not be backed up")
	}

	// The id comes first: a backend is bound to one (volume, savepoint) pair, so
	// there is nothing to build it from until the savepoint has one.
	createdAt := time.Now()
	savepointID := strconv.FormatInt(createdAt.Unix(), 10)

	stats, err := s.snapshotVolumes(ctx, snapshotTarget{
		sessionStore: s.savepointStore,
		savepointID:  savepointID,
	}, volumes)
	if err != nil {
		return model.Savepoint{}, err
	}

	return model.Savepoint{
		ID:          savepointID,
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   createdAt,
		Stats:       stats,
	}, nil
}

// snapshotTarget says where a fan-out lands. Which backend is absent on purpose:
// that is the volume's own Backend.
type snapshotTarget struct {
	sessionStore storages.Storager
	savepointID  string
}

// snapshotVolumes mints one backend per volume and fans out, returning one stat
// per volume in input order. Every backend is built before any snapshot runs, so
// a volume with no registered backend fails before a single byte moves.
func (s *Service) snapshotVolumes(ctx context.Context, target snapshotTarget, volumes []model.Volume) ([]model.SnapshotStat, error) {
	backends := make([]VolumeSnapshotBackend, len(volumes))
	for i, volume := range volumes {
		backend, err := s.snapshotBackendRegistry.New(BackendSpec{
			SessionStore: target.sessionStore,
			SavepointID:  target.savepointID,
			Volume:       volume,
		})
		if err != nil {
			return nil, fmt.Errorf("building %s backend for volume %s: %w", volume.Backend, volume.ObjectID, err)
		}
		backends[i] = backend
	}

	stats := make([]model.SnapshotStat, len(volumes))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxConcurrentSnapshots)
	for i, backend := range backends {
		eg.Go(func() error {
			stat, err := backend.Snapshot(egCtx)
			if err != nil {
				return fmt.Errorf("snapshotting volume %s: %w", volumes[i].ObjectID, err)
			}
			stats[i] = stat
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return stats, nil
}
