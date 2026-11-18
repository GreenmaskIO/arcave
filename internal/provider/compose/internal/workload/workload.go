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

// Package workload is the provider's stages (prepare, deploy, capture, hold) over one compose project.
package workload

import (
	"context"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/capture"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/captureplan"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/deploy"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/inspection"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/prepare"
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

// composeIntrospector reads the live compose project as the daemon holds it, in full or by its
// label alone.
type composeIntrospector interface {
	Introspect(ctx context.Context) (composemodel.IntrospectionResult, error)
	ReadResources(ctx context.Context) (composemodel.ComposeProjectResources, error)
}

// projectAssembler builds a compose project's spec out of its introspection.
type projectAssembler interface {
	Assemble(ctx context.Context, introspection composemodel.IntrospectionResult) (*types.Project, error)
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
	CaptureImage(ctx context.Context, req composemodel.ImageCaptureRequest) (model.Image, error)
	CaptureWritableLayer(ctx context.Context, req composemodel.WritableLayerCaptureRequest) (model.WritableLayer, error)
	RestoreWritableLayer(ctx context.Context, req composemodel.WritableLayerRestoreRequest) (model.WritableLayerRestoreResult, error)
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
	// DownWithVolumes removes everything the daemon holds of the compose project, its volumes too,
	// by the project's name alone.
	DownWithVolumes(ctx context.Context, composeProject string) error
	// Create lets compose recreate a volume whose config drifted only when restoredVolumes, the
	// docker names of the volumes the rollback restores, holds it.
	Create(ctx context.Context, project *types.Project, restoredVolumes []string) error
}

// volumeRemover reaps the volumes a teardown orphans, or a failed build created.
type volumeRemover interface {
	RemoveVolume(ctx context.Context, name string) error
}

// projectRemover removes by docker name the containers and networks compose's down leaves of a
// dropped compose project.
type projectRemover interface {
	RemoveContainer(ctx context.Context, name string) error
	RemoveNetwork(ctx context.Context, name string) error
}

// layerTags lists the arcave/…:sp-<savepoint> tags the daemon holds and takes one off, never
// forced.
type layerTags interface {
	ListLayerImages(ctx context.Context) ([]composemodel.TaggedImage, error)
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// containerRebuilder recreates one built container from another image as the replica a
// savepoint recorded, which compose's one image per service cannot express.
type containerRebuilder interface {
	RecreateWithImage(ctx context.Context, containerID string, as composemodel.ReplicaIdentity, imageRef string) (string, error)
}

// imageLookup tells whether the daemon holds an image under a reference.
type imageLookup interface {
	ImageRefExists(ctx context.Context, ref string) (bool, error)
}

// containerControl freezes containers for a capture and resumes them after, and runs the hooks in
// them.
type containerControl interface {
	PauseContainers(ctx context.Context, ids []string) error
	// UnpauseContainers resumes whichever of the containers is paused; one that is not, or is
	// gone, is no error.
	UnpauseContainers(ctx context.Context, ids []string) error
	// AwaitHealthProbes waits out the unhealthy a pause leaves on the containers until their next
	// probe, bounded by their healthchecks.
	AwaitHealthProbes(ctx context.Context, ids []string) error
	// ExecInContainer runs spec in the container and waits for it to exit; a container not
	// running is composemodel.ErrContainerNotRunning.
	ExecInContainer(ctx context.Context, id string, spec composemodel.ExecSpec) (composemodel.ExecResult, error)
}

// Deps are the collaborators of a compose project's workload.
type Deps struct {
	SavepointRepo savepointRepository
	ComposeReader composeIntrospector
	ImageBackend  imageBackend
	ComposeRunner composeRunner
	// ContainerControl pauses the services captured with model.CaptureModePause, resumes a
	// replica found frozen, and runs the hooks.
	ContainerControl containerControl
	VolumeRemover    volumeRemover
	// ProjectRemover removes by docker name what compose's down leaves of a dropped compose
	// project.
	ProjectRemover     projectRemover
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
	// DaemonVersion is the version the engine reports; Inspect judges it.
	DaemonVersion string
	// MinDaemonMajor is the oldest major of that engine a workflow runs on; 0 requires none.
	MinDaemonMajor int
	// Store is the compose project's store.
	Store storages.Storager
	// LayerTags are the restored layer tags a drop removes; LayerOrigin names the compose project
	// it takes down by name and whose tags those are.
	LayerTags   layerTags
	LayerOrigin composemodel.LayerOrigin
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency int
}

// composeWorkload is one compose project in one session, as the compose provider runs it: the
// workload core inspects, over the components each stage delegates to.
type composeWorkload struct {
	savepointRepo savepointRepository
	inspector     *inspection.Inspector
	planner       *captureplan.Planner
	preparer      *prepare.Preparer
	deployer      *deploy.Deployer
	capturer      *capture.Capturer
	// composeRunner and containerControl hold a project still for a capture and release it.
	composeRunner    composeRunner
	containerControl containerControl
}

// New builds the workload of one compose project from deps.
func New(deps Deps) provider.Workload {
	return newComposeWorkload(deps)
}

// newComposeWorkload builds the workload and the components it delegates to.
func newComposeWorkload(deps Deps) *composeWorkload {
	inspector := inspection.New(inspection.Deps{
		ComposeReader:  deps.ComposeReader,
		DaemonVersion:  deps.DaemonVersion,
		MinDaemonMajor: deps.MinDaemonMajor,
	})
	return &composeWorkload{
		savepointRepo: deps.SavepointRepo,
		inspector:     inspector,
		planner: captureplan.New(captureplan.Deps{
			ProjectAssembler: deps.ProjectAssembler,
			SpecCodec:        deps.SpecCodec,
			BackendRegistry:  deps.BackendRegistry,
			CaptureDefaults:  deps.CaptureDefaults,
			Store:            deps.Store,
		}),
		preparer: prepare.New(prepare.Deps{
			SpecReader:      deps.SavepointRepo,
			SpecParser:      deps.SpecCodec,
			LayerRestorer:   deps.ImageBackend,
			ImageLookup:     deps.ImageLookup,
			BackendRegistry: deps.BackendRegistry,
			Store:           deps.Store,
			LeftoverVolumes: deps.LeftoverVolumes,
			Concurrency:     deps.Concurrency,
		}),
		deployer: deploy.New(deploy.Deps{
			ComposeRunner:      deps.ComposeRunner,
			ContainerControl:   deps.ContainerControl,
			ContainerRebuilder: deps.ContainerRebuilder,
			Inspector:          inspector,
			ComposeReader:      deps.ComposeReader,
			VolumeRemover:      deps.VolumeRemover,
			ProjectRemover:     deps.ProjectRemover,
			LayerTags:          deps.LayerTags,
			LayerOrigin:        deps.LayerOrigin,
			Concurrency:        deps.Concurrency,
		}),
		capturer: capture.New(capture.Deps{
			SavepointRepo: deps.SavepointRepo,
			ImageBackend:  deps.ImageBackend,
			Concurrency:   deps.Concurrency,
		}),
		composeRunner:    deps.ComposeRunner,
		containerControl: deps.ContainerControl,
	}
}

// Inspect reads the compose project as the daemon holds it now, and refuses a daemon the image
// backend cannot serve.
func (s *composeWorkload) Inspect(ctx context.Context) (provider.LiveWorkload, error) {
	introspection, err := s.inspector.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	return liveWorkload{
		service:       s,
		introspection: introspection,
	}, nil
}

// Survey reads what the daemon holds of the compose project by its label.
func (s *composeWorkload) Survey(ctx context.Context) (provider.SurveyedWorkload, error) {
	resources, err := s.inspector.ReadResources(ctx)
	if err != nil {
		return nil, err
	}
	return surveyedWorkload{
		service:   s,
		resources: resources,
	}, nil
}
