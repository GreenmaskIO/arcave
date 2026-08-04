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

// Service orchestrates arcave's savepoint workflows over one root. A bare "store"
// is the right name again — there was a second root once, an undo tree sharing
// this one's type, and only the names could keep the two apart.
//
// Every record travels with its data, in a repository over that one store. A
// rollback's safety snapshot is an ordinary savepoint there, marked
// model.SafetySnapshotLabel: hidden from a scanning selector, findable by id.
type Service struct {
	savepointRepo           savepointRepository
	projectLocker           projectLocker
	composeInspector        composeIntrospector
	composeRunner           composeRunner
	snapshotBackendRegistry model.SnapshotBackendRegistry
	store                   storages.Storager
	defaultBackend          model.SnapshotBackendKind
	maxConcurrentOperations int
}

func New(
	savepointRepo savepointRepository,
	projectLocker projectLocker,
	composeInspector composeIntrospector,
	composeRunner composeRunner,
	snapshotBackendRegistry model.SnapshotBackendRegistry,
	store storages.Storager,
	defaultBackend model.SnapshotBackendKind,
) *Service {
	return &Service{
		savepointRepo:           savepointRepo,
		projectLocker:           projectLocker,
		composeInspector:        composeInspector,
		composeRunner:           composeRunner,
		snapshotBackendRegistry: snapshotBackendRegistry,
		store:                   store,
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
