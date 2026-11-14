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

package compose

import (
	"context"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/capture"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// savepointRepository writes a capture's spec and commits its record, and reads records and specs
// by id.
type savepointRepository interface {
	SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error
	SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error
	// LoadSavepoints returns the committed records of ids; one not found is left out.
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error)
	LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error)
}

// composeIntrospector reads the live compose project as the daemon holds it.
type composeIntrospector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// projectAssembler builds a compose project's spec out of its introspection.
type projectAssembler interface {
	Assemble(ctx context.Context, introspection model.IntrospectionResult) (*types.Project, error)
}

// specCodec moves a spec through the artifact a savepoint commits; Parse is compose's loader, so
// every project the runner acts on has been through it.
type specCodec interface {
	Marshal(project *types.Project) ([]byte, error)
	Parse(ctx context.Context, spec []byte) (*types.Project, error)
}

// imageBackend commits writable layers into the store and keeps the images they were taken over,
// and loads a layer back out of the store under a tag of its own.
type imageBackend interface {
	CaptureImage(ctx context.Context, req model.ImageCaptureRequest) (model.Image, error)
	CaptureWritableLayer(ctx context.Context, req model.WritableLayerCaptureRequest) (model.WritableLayer, error)
	RestoreWritableLayer(ctx context.Context, req model.WritableLayerRestoreRequest) (model.WritableLayerRestoreResult, error)
}

// composeRunner is the compose project's lifecycle: stop it for a capture and start it after, tear
// it down, build it back from a spec without starting it.
type composeRunner interface {
	// Stop stops every service of project and returns the ids of the containers its deadline
	// killed.
	Stop(ctx context.Context, project *types.Project) ([]string, error)
	// StopServices stops the named services alone, and returns the same.
	StopServices(ctx context.Context, project *types.Project, services []string) ([]string, error)
	// Start starts every service of project, whichever of them was stopped, and waits on their
	// health checks; a running container is left as it is.
	Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error
	Down(ctx context.Context, project *types.Project) error
	// Create lets compose recreate a volume whose config drifted only when restoredVolumes, the
	// docker names of the volumes the rollback restores, holds it.
	Create(ctx context.Context, project *types.Project, restoredVolumes []string) error
}

// volumeRemover reaps the volumes a teardown orphans, or a failed build created.
type volumeRemover interface {
	RemoveVolume(ctx context.Context, name string) error
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

// containerControl freezes containers for a capture and resumes them after.
type containerControl interface {
	PauseContainers(ctx context.Context, ids []string) error
	// UnpauseContainers resumes whichever of the containers is paused; one that is not, or is
	// gone, is no error.
	UnpauseContainers(ctx context.Context, ids []string) error
	// AwaitHealthProbes waits out the unhealthy a pause leaves on the containers until their next
	// probe, bounded by their healthchecks.
	AwaitHealthProbes(ctx context.Context, ids []string) error
}

// Deps are Provider's collaborators.
type Deps struct {
	SavepointRepo    savepointRepository
	ComposeInspector composeIntrospector
	ImageBackend     imageBackend
	ComposeRunner    composeRunner
	// ContainerControl pauses the services captured with model.CaptureModePause, and resumes a
	// replica found frozen.
	ContainerControl   containerControl
	VolumeRemover      volumeRemover
	ContainerRebuilder containerRebuilder
	ImageLookup        imageLookup
	ProjectAssembler   projectAssembler
	SpecCodec          specCodec
	BackendRegistry    model.SnapshotBackendRegistry
	// CaptureDefaults are the backends a unit no arcave.* label names is captured by.
	CaptureDefaults capturepolicy.CaptureDefaults
	// LeftoverVolumes is what preparing a savepoint over an absent compose project does with a
	// volume that outlived it and the build overwrites; empty is model.LeftoverVolumesOverwrite.
	LeftoverVolumes model.LeftoverVolumesPolicy
	// DaemonVersion is the version the engine reports; requireDaemon judges it.
	DaemonVersion string
	// MinDaemonMajor is the oldest major of that engine a workflow runs on; 0 requires none.
	MinDaemonMajor int
	// Store is the compose project's store: the capture's tree and its heartbeat.
	Store storages.Storager
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency int
	// RunnerInfo identifies this process in a capture's heartbeat.
	RunnerInfo heartbeat.RunnerInfo
	// HeartbeatInterval and HeartbeatStaleAfter time a capture's heartbeat; zero keeps
	// heartbeat.DefaultInterval and heartbeat.DefaultStaleAfter.
	HeartbeatInterval   time.Duration
	HeartbeatStaleAfter time.Duration
}

// Provider is the compose provider of one compose project in one session: the workload core inspects.
type Provider struct {
	savepointRepo           savepointRepository
	composeInspector        composeIntrospector
	imageBackend            imageBackend
	composeRunner           composeRunner
	containerControl        containerControl
	volumeRemover           volumeRemover
	containerRebuilder      containerRebuilder
	imageLookup             imageLookup
	leftoverVolumes         model.LeftoverVolumesPolicy
	daemonVersion           string
	minDaemonMajor          int
	projectAssembler        projectAssembler
	specCodec               specCodec
	snapshotBackendRegistry model.SnapshotBackendRegistry
	captureDefaults         capturepolicy.CaptureDefaults
	store                   storages.Storager
	maxConcurrentOperations int
	capturer                *capture.Capturer
}

// New builds a Provider from deps.
func New(deps Deps) *Provider {
	return &Provider{
		savepointRepo:           deps.SavepointRepo,
		composeInspector:        deps.ComposeInspector,
		imageBackend:            deps.ImageBackend,
		composeRunner:           deps.ComposeRunner,
		containerControl:        deps.ContainerControl,
		volumeRemover:           deps.VolumeRemover,
		containerRebuilder:      deps.ContainerRebuilder,
		imageLookup:             deps.ImageLookup,
		leftoverVolumes:         deps.LeftoverVolumes,
		daemonVersion:           deps.DaemonVersion,
		minDaemonMajor:          deps.MinDaemonMajor,
		projectAssembler:        deps.ProjectAssembler,
		specCodec:               deps.SpecCodec,
		snapshotBackendRegistry: deps.BackendRegistry,
		captureDefaults:         deps.CaptureDefaults,
		store:                   deps.Store,
		maxConcurrentOperations: fanOutLimit(deps.Concurrency),
		capturer: capture.New(capture.Deps{
			SavepointRepo:       deps.SavepointRepo,
			ImageBackend:        deps.ImageBackend,
			Store:               deps.Store,
			Concurrency:         deps.Concurrency,
			RunnerInfo:          deps.RunnerInfo,
			HeartbeatInterval:   deps.HeartbeatInterval,
			HeartbeatStaleAfter: deps.HeartbeatStaleAfter,
		}),
	}
}

// fanOutLimit turns a configured concurrency into an errgroup limit, where a
// negative one is unbounded and zero would admit no goroutine at all.
func fanOutLimit(concurrency int) int {
	if concurrency < 1 {
		return -1
	}
	return concurrency
}
