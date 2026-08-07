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

// inflightMarkers is the intent marker's seam: written before a rollback's first
// destructive write, cleared when the environment is consistent again.
type inflightMarkers interface {
	Get(ctx context.Context) (marker model.InflightMarker, found bool, err error)
	Put(ctx context.Context, marker model.InflightMarker) error
	Clear(ctx context.Context) error
}

// Deps are Service's collaborators. A struct rather than a parameter list: the
// seams have no natural order, so a new one could silently swap with an existing one.
type Deps struct {
	SavepointRepo    savepointRepository
	ProjectLocker    projectLocker
	ComposeInspector composeIntrospector
	ComposeRunner    composeRunner
	BackendRegistry  model.SnapshotBackendRegistry
	Inflight         inflightMarkers
	Store            storages.Storager
	DefaultBackend   model.SnapshotBackendKind
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
	inflightMarker          inflightMarkers
	store                   storages.Storager
	defaultBackend          model.SnapshotBackendKind
	maxConcurrentOperations int
}

func New(d Deps) *Service {
	return &Service{
		savepointRepo:           d.SavepointRepo,
		projectLocker:           d.ProjectLocker,
		composeInspector:        d.ComposeInspector,
		composeRunner:           d.ComposeRunner,
		snapshotBackendRegistry: d.BackendRegistry,
		inflightMarker:          d.Inflight,
		store:                   d.Store,
		defaultBackend:          d.DefaultBackend,
		maxConcurrentOperations: maxConcurrentOperations,
	}
}

// withUnlock runs fn with the project lock held, and releases it however fn ends.
// Releasing is best-effort: a failed release is logged, never returned.
//
// A function rather than a method, because Go has no generic methods: every
// workflow returns its own result type, and the lock must not decide it.
func withUnlock[T any](ctx context.Context, s *Service, fn func() (T, error)) (T, error) {
	release, err := s.projectLocker.Acquire(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	defer func() {
		if err := release(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("release project lock")
		}
	}()
	return fn()
}
