package core

import (
	"context"
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

// maxConcurrentOperations caps how many volumes one fan-out works at once.
const maxConcurrentOperations = 5

type savepointRepository interface {
	ListSavepoints(ctx context.Context) ([]model.Savepoint, error)
	SaveSavepoint(ctx context.Context, record model.Savepoint) error
	SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error
	LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error)
	DeleteSavepoint(ctx context.Context, id model.SavepointID) error
}

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// composeIntrospector reports the live stack it was bound to at construction.
type composeIntrospector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// projectAssembler materialises the spec the runner acts on — out of a live
// stack, or out of the artifact a savepoint committed.
type projectAssembler interface {
	Assemble(res model.IntrospectionResult) (*types.Project, error)
	Marshal(project *types.Project) ([]byte, error)
	Parse(ctx context.Context, spec []byte) (*types.Project, error)
}

// composeRunner is the stack's lifecycle: stop it, tear it down, build it back
// from a spec without starting it, start it.
type composeRunner interface {
	Stop(ctx context.Context, project *types.Project) error
	Down(ctx context.Context, project *types.Project) error
	Create(ctx context.Context, project *types.Project) error
	Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error
}

// volumeRemover reaps the anonymous volumes a teardown orphans.
type volumeRemover interface {
	RemoveVolume(ctx context.Context, name string) error
}

// imageCapturer commits one stopped container's writable layer into the store.
type imageCapturer interface {
	Capture(ctx context.Context, req model.ImageCaptureRequest) (model.ImageCapture, error)
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
	ProjectAssembler projectAssembler
	ComposeRunner    composeRunner
	VolumeRemover    volumeRemover
	ImageCapturer    imageCapturer
	BackendRegistry  model.SnapshotBackendRegistry
	Inflight         inflightMarkers
	Store            storages.Storager
	DefaultBackend   model.SnapshotBackendKind
}

// Service orchestrates arcave's savepoint workflows over one store root.
//
// Every record travels with its data, in a repository over that one store. A
// rollback's safety snapshot is an ordinary savepoint there, marked
// model.SafetySnapshotLabel: hidden from a scanning selector, findable by id.
type Service struct {
	savepointRepo           savepointRepository
	projectLocker           projectLocker
	composeInspector        composeIntrospector
	projectAssembler        projectAssembler
	composeRunner           composeRunner
	volumeRemover           volumeRemover
	imageCapturer           imageCapturer
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
		projectAssembler:        d.ProjectAssembler,
		composeRunner:           d.ComposeRunner,
		volumeRemover:           d.VolumeRemover,
		imageCapturer:           d.ImageCapturer,
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
