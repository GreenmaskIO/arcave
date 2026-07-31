package core

import (
	"context"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

// maxConcurrentOperations caps how many volumes one fan-out works at once.
const maxConcurrentOperations = 5

type savepointRepository interface {
	List(ctx context.Context) ([]model.Savepoint, error)
	Save(ctx context.Context, record model.Savepoint) error
	Delete(ctx context.Context, id model.SavepointID) error
}

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

type composeIntrospector interface {
	Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error)
}

type composeRunner interface {
	Stop(ctx context.Context, files ...string) error
	Up(ctx context.Context, waitTimeout time.Duration, files ...string) error
}

// Service orchestrates arcave's savepoint workflows. The savepoint tier keeps a
// repository beside its store, so a savepoint's record travels with its data; a
// rollback's safety backup has no record of its own — it lives for the length of
// one rollback, in this process's memory.
type Service struct {
	savepointRepo           savepointRepository
	projectLocker           projectLocker
	composeInspector        composeIntrospector
	composeRunner           composeRunner
	snapshotBackendRegistry model.SnapshotBackendRegistry
	savepointStore          storages.Storager
	safetyBackupStore       storages.Storager
	defaultBackend          model.SnapshotBackendKind
	maxConcurrentOperations int
}

func New(
	savepointRepo savepointRepository,
	projectLocker projectLocker,
	composeInspector composeIntrospector,
	composeRunner composeRunner,
	snapshotBackendRegistry model.SnapshotBackendRegistry,
	savepointStore storages.Storager,
	safetyBackupStore storages.Storager,
	defaultBackend model.SnapshotBackendKind,
) *Service {
	return &Service{
		savepointRepo:           savepointRepo,
		projectLocker:           projectLocker,
		composeInspector:        composeInspector,
		composeRunner:           composeRunner,
		snapshotBackendRegistry: snapshotBackendRegistry,
		savepointStore:          savepointStore,
		safetyBackupStore:       safetyBackupStore,
		defaultBackend:          defaultBackend,
		maxConcurrentOperations: maxConcurrentOperations,
	}
}

// lockProject claims the project for a mutating workflow and returns the release
// to defer. Releasing is best-effort: the returned func logs its own error.
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
