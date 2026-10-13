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
	"context"
	"fmt"
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

// composeIntrospector reports the live compose project it was bound to at construction.
type composeIntrospector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// projectAssembler builds the spec of a live compose project out of its introspection.
type projectAssembler interface {
	Assemble(ctx context.Context, res model.IntrospectionResult) (*types.Project, error)
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

// imageBackend carries one container's writable layer both ways: committed into
// the store, and loaded back out of it under a tag of its own.
type imageBackend interface {
	Capture(ctx context.Context, req model.ImageCaptureRequest) (model.ImageCapture, error)
	CaptureUpstream(ctx context.Context, req model.UpstreamImageCaptureRequest) (model.UpstreamImage, error)
	Restore(ctx context.Context, req model.ImageRestoreRequest) (model.ImageRestore, error)
}

// containerRebuilder recreates one built container from another image as the replica a
// savepoint recorded, which compose's one image per service cannot express.
type containerRebuilder interface {
	RecreateWithImage(ctx context.Context, containerID string, as model.ReplicaIdentity, imageRef string) (string, error)
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
	SavepointRepo      savepointRepository
	ProjectLocker      projectLocker
	ComposeInspector   composeIntrospector
	ProjectAssembler   projectAssembler
	SpecCodec          specCodec
	ComposeRunner      composeRunner
	VolumeRemover      volumeRemover
	ImageBackend       imageBackend
	ContainerRebuilder containerRebuilder
	BackendRegistry    model.SnapshotBackendRegistry
	Inflight           inflightMarkers
	Store              storages.Storager
	DefaultBackend     model.SnapshotBackendKind
	// Session is the session Store belongs to; zero means model.DefaultSession.
	Session model.SessionID
	// ComposeProject is the compose project Store belongs to.
	ComposeProject model.DockerComposeProjectName
	// Sessions checks, under the lock, that Session still exists.
	Sessions sessionLookup
	// MinDaemonMajor is the oldest Docker major the image backend can serve; 0
	// requires none.
	MinDaemonMajor int
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
	specCodec               specCodec
	composeRunner           composeRunner
	volumeRemover           volumeRemover
	imageBackend            imageBackend
	containerRebuilder      containerRebuilder
	snapshotBackendRegistry model.SnapshotBackendRegistry
	inflightMarker          inflightMarkers
	store                   storages.Storager
	defaultBackend          model.SnapshotBackendKind
	session                 model.SessionID
	composeProject          model.DockerComposeProjectName
	sessions                sessionLookup
	minDaemonMajor          int
	maxConcurrentOperations int
}

func New(d Deps) *Service {
	if d.Session.IsNil() {
		d.Session = model.DefaultSession
	}
	return &Service{
		savepointRepo:           d.SavepointRepo,
		projectLocker:           d.ProjectLocker,
		composeInspector:        d.ComposeInspector,
		projectAssembler:        d.ProjectAssembler,
		specCodec:               d.SpecCodec,
		composeRunner:           d.ComposeRunner,
		volumeRemover:           d.VolumeRemover,
		imageBackend:            d.ImageBackend,
		containerRebuilder:      d.ContainerRebuilder,
		snapshotBackendRegistry: d.BackendRegistry,
		inflightMarker:          d.Inflight,
		store:                   d.Store,
		defaultBackend:          d.DefaultBackend,
		session:                 d.Session,
		composeProject:          d.ComposeProject,
		sessions:                d.Sessions,
		minDaemonMajor:          d.MinDaemonMajor,
		maxConcurrentOperations: maxConcurrentOperations,
	}
}

// locked runs a workflow with the project lock held, once its session is known to still
// exist: one deleted since the command resolved it must not be written into again.
func locked[T any](ctx context.Context, s *Service, fn func() (T, error)) (T, error) {
	return withUnlock(ctx, s.projectLocker, func() (T, error) {
		if err := s.requireSession(ctx); err != nil {
			var zero T
			return zero, err
		}
		return fn()
	})
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

// requireSession refuses a session other than the default that has no record.
func (s *Service) requireSession(ctx context.Context) error {
	if s.session == model.DefaultSession {
		return nil
	}
	_, found, err := s.sessions.LoadSession(ctx, s.session)
	if err != nil {
		return fmt.Errorf("checking session %s: %w", s.session, err)
	}
	if !found {
		return fmt.Errorf("%w: %s", model.ErrSessionNotFound, s.session)
	}
	return nil
}
