// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

type savepointRepository interface {
	ListSavepoints(ctx context.Context) ([]model.Savepoint, error)
	SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error
	SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error
	LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error)
	DeleteSavepoint(ctx context.Context, id model.SavepointID) error
}

// SavepointReader reads the savepoints of one compose project's store, for a service that
// reads across compose projects; the DI root opens one per store.
type SavepointReader interface {
	ListSavepoints(ctx context.Context) ([]model.Savepoint, error)
}

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// composeIntrospector reports the live compose project it was bound to at construction.
type composeIntrospector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// projectAssembler builds the spec of a live compose project out of its introspection.
type projectAssembler interface {
	Assemble(ctx context.Context, introspection model.IntrospectionResult) (*types.Project, error)
}

// specCodec moves a spec through the artifact a savepoint commits; Parse is compose's
// loader, so every project the runner acts on has been through it.
type specCodec interface {
	Marshal(project *types.Project) ([]byte, error)
	Parse(ctx context.Context, spec []byte) (*types.Project, error)
}

// composeRunner is the compose project's lifecycle: stop it, tear it down, build it back
// from a spec without starting it, start it.
type composeRunner interface {
	// Stop returns the ids of the containers its deadline killed.
	Stop(ctx context.Context, project *types.Project) ([]string, error)
	Down(ctx context.Context, project *types.Project) error
	Create(ctx context.Context, project *types.Project) error
	Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error
}

// volumeRemover reaps the anonymous volumes a teardown orphans.
type volumeRemover interface {
	RemoveVolume(ctx context.Context, name string) error
}

// imageBackend carries writable layers both ways — committed into the store, and loaded
// back out of it under a tag of its own — and keeps the images they were taken over.
type imageBackend interface {
	CaptureWritableLayer(ctx context.Context, req model.WritableLayerCaptureRequest) (model.WritableLayer, error)
	CaptureImage(ctx context.Context, req model.ImageCaptureRequest) (model.Image, error)
	RestoreWritableLayer(ctx context.Context, req model.WritableLayerRestoreRequest) (model.WritableLayerRestoreResult, error)
}

// containerRebuilder recreates one built container from another image as the replica a
// savepoint recorded, which compose's one image per service cannot express.
type containerRebuilder interface {
	RecreateWithImage(ctx context.Context, containerID string, as model.ReplicaIdentity, imageRef string) (string, error)
}

// imageLookup tells whether the daemon holds an image under a reference.
type imageLookup interface {
	ImageRefExists(ctx context.Context, ref string) (bool, error)
}

// sessionLookup finds a session's record; core re-checks it under the lock.
type sessionLookup interface {
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
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
	SavepointRepo savepointRepository
	ProjectLocker projectLocker
	// SessionLocker holds Session's lock in the store for the whole of a workflow.
	SessionLocker      model.SessionLocker
	ComposeInspector   composeIntrospector
	ProjectAssembler   projectAssembler
	SpecCodec          specCodec
	ComposeRunner      composeRunner
	VolumeRemover      volumeRemover
	ImageBackend       imageBackend
	ContainerRebuilder containerRebuilder
	ImageLookup        imageLookup
	BackendRegistry    model.SnapshotBackendRegistry
	Inflight           inflightMarkers
	Store              storages.Storager
	// CaptureDefaults are the backends a unit no arcave.* label names is captured by.
	CaptureDefaults capturepolicy.CaptureDefaults
	// Session is the session Store belongs to; zero means model.DefaultSession.
	Session model.SessionID
	// ComposeProject is the compose project Store belongs to.
	ComposeProject model.DockerComposeProjectName
	// Sessions checks, under the lock, that Session still exists.
	Sessions sessionLookup
	// MinDaemonMajor is the oldest Docker major a workflow runs on; 0 requires none.
	MinDaemonMajor int
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency int
	// RunnerInfo identifies this process in a capture's heartbeat.
	RunnerInfo heartbeat.RunnerInfo
	// HeartbeatInterval and HeartbeatStaleAfter time a capture's heartbeat; zero keeps
	// heartbeat.DefaultInterval and heartbeat.DefaultStaleAfter.
	HeartbeatInterval   time.Duration
	HeartbeatStaleAfter time.Duration
}

// Service orchestrates arcave's savepoint workflows over one store root.
//
// Every record travels with its data, in a repository over that one store. A
// rollback's safety snapshot is an ordinary savepoint there, marked
// model.SavepointLabelSafetySnapshot: hidden from a scanning selector, findable by id.
type Service struct {
	savepointRepo           savepointRepository
	projectLocker           projectLocker
	sessionLocker           model.SessionLocker
	composeInspector        composeIntrospector
	projectAssembler        projectAssembler
	specCodec               specCodec
	composeRunner           composeRunner
	volumeRemover           volumeRemover
	imageBackend            imageBackend
	containerRebuilder      containerRebuilder
	imageLookup             imageLookup
	snapshotBackendRegistry model.SnapshotBackendRegistry
	inflightMarker          inflightMarkers
	store                   storages.Storager
	captureDefaults         capturepolicy.CaptureDefaults
	session                 model.SessionID
	composeProject          model.DockerComposeProjectName
	sessions                sessionLookup
	minDaemonMajor          int
	maxConcurrentOperations int
	runnerInfo              heartbeat.RunnerInfo
	heartbeatInterval       time.Duration
	heartbeatStaleAfter     time.Duration
}

func New(deps Deps) *Service {
	if deps.Session.IsNil() {
		deps.Session = model.DefaultSession
	}
	return &Service{
		savepointRepo:           deps.SavepointRepo,
		projectLocker:           deps.ProjectLocker,
		sessionLocker:           deps.SessionLocker,
		composeInspector:        deps.ComposeInspector,
		projectAssembler:        deps.ProjectAssembler,
		specCodec:               deps.SpecCodec,
		composeRunner:           deps.ComposeRunner,
		volumeRemover:           deps.VolumeRemover,
		imageBackend:            deps.ImageBackend,
		containerRebuilder:      deps.ContainerRebuilder,
		imageLookup:             deps.ImageLookup,
		snapshotBackendRegistry: deps.BackendRegistry,
		inflightMarker:          deps.Inflight,
		store:                   deps.Store,
		captureDefaults:         deps.CaptureDefaults,
		session:                 deps.Session,
		composeProject:          deps.ComposeProject,
		sessions:                deps.Sessions,
		minDaemonMajor:          deps.MinDaemonMajor,
		maxConcurrentOperations: fanOutLimit(deps.Concurrency),
		runnerInfo:              deps.RunnerInfo,
		heartbeatInterval:       cmp.Or(deps.HeartbeatInterval, heartbeat.DefaultInterval),
		heartbeatStaleAfter:     cmp.Or(deps.HeartbeatStaleAfter, heartbeat.DefaultStaleAfter),
	}
}

// locked runs a workflow with the project lock and the session's lock held, once its session is
// known to still exist: one deleted since the command resolved it must not be written into again.
// fn gets the session lock and its context.
func locked[T any](
	ctx context.Context, s *Service, operation model.SessionLockOperation,
	fn func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error),
) (T, error) {
	return withUnlock(ctx, s.projectLocker, func() (T, error) {
		return withSessionLock(ctx, s.sessionLocker, operation, func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error) {
			if err := requireSession(ctx, s.sessions, s.session); err != nil {
				var zero T
				return zero, err
			}
			return fn(ctx, grantedLock)
		})
	})
}

// withSessionLock runs fn holding the session's lock in the store, and unlocks it however fn
// ends; a failed unlock is logged. fn runs on the lock's context: a lost lock cancels it with
// model.ErrSessionLockLost as its cause, which fn reports as any interrupt.
func withSessionLock[T any](
	ctx context.Context,
	locker model.SessionLocker,
	operation model.SessionLockOperation,
	fn func(ctx context.Context, grantedLock model.GrantedSessionLock) (T, error),
) (T, error) {
	// The wait is a node of its own, shown only once another holds the lock; the workflow goes on
	// under the node ctx carries.
	waitCtx, wait := progress.Begin(ctx, progress.Step{Kind: kindSessionLock})
	lockCtx, grantedLock, err := locker.Lock(waitCtx, operation)
	wait.End(err)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("lock session: %w", err)
	}
	defer func() {
		if err := grantedLock.Unlock(ctx); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("unlock session")
		}
	}()
	return fn(progress.WithNode(lockCtx, progress.Current(ctx)), grantedLock)
}

// withUnlock runs fn with the project lock held, and releases it however fn ends.
// Releasing is best-effort: a failed release is logged, never returned.
//
// A function rather than a method, because Go has no generic methods: every
// workflow returns its own result type, and the lock must not decide it.
func withUnlock[T any](ctx context.Context, locker projectLocker, fn func() (T, error)) (T, error) {
	release, err := locker.Acquire(ctx)
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

// requireSession refuses a session other than the default that has no record: one resolved
// outside the lock may have been removed since.
func requireSession(ctx context.Context, sessions sessionLookup, id model.SessionID) error {
	if id == model.DefaultSession {
		return nil
	}
	_, found, err := sessions.LoadSession(ctx, id)
	if err != nil {
		return fmt.Errorf("checking session %s: %w", id, err)
	}
	if !found {
		return fmt.Errorf("%w: %s", model.ErrSessionNotFound, id)
	}
	return nil
}

// fanOutLimit turns a configured concurrency into an errgroup limit, where a
// negative one is unbounded and zero would admit no goroutine at all.
func fanOutLimit(concurrency int) int {
	if concurrency < 1 {
		return -1
	}
	return concurrency
}
