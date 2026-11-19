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

// Package deploy builds a compose project back from a prepared savepoint and runs it: down, create,
// the replicas compose cannot name rebuilt, the volumes restored, start, and what a failure or a
// teardown leaves behind removed.
package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/fanout"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps a deploy opens: <namespace>.<action> for an action, a bare noun for an
// object.
const (
	kindComposeDown   progress.Kind = "compose.down"
	kindComposeCreate progress.Kind = "compose.create"
	kindComposeStart  progress.Kind = "compose.start"

	kindVolumeRestore  progress.Kind = "volume.restore"
	kindVolume         progress.Kind = "volume"
	kindReplicaRebuild progress.Kind = "replica.rebuild"
	kindReplica        progress.Kind = "replica"

	kindDownUntag progress.Kind = "down.untag"
	kindTag       progress.Kind = "tag"
)

// composeRunner takes a compose project down, creates it without starting it, and starts it.
type composeRunner interface {
	Down(ctx context.Context, project *types.Project) error
	// DownWithVolumes removes everything the daemon holds of the compose project, its volumes too,
	// by the project's name alone.
	DownWithVolumes(ctx context.Context, composeProject string) error
	// Create lets compose recreate a volume whose config drifted only when restoredVolumes, the
	// docker names of the volumes the rollback restores, holds it.
	Create(ctx context.Context, project *types.Project, restoredVolumes []string) error
	// Start starts every service of project and waits on their health checks.
	Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error
}

// containerResumer resumes frozen containers; one that is not paused, or is gone, is no error.
type containerResumer interface {
	UnpauseContainers(ctx context.Context, ids []string) error
}

// containerRebuilder recreates one built container from another image as the replica a
// savepoint recorded, which compose's one image per service cannot express.
type containerRebuilder interface {
	RecreateWithImage(ctx context.Context, containerID string, as composemodel.ReplicaIdentity, imageRef string) (string, error)
}

// composeIntrospector reads the compose project as the daemon holds it.
type composeIntrospector interface {
	Introspect(ctx context.Context) (composemodel.IntrospectionResult, error)
}

// resourceReader reads what the daemon holds of the compose project by its label alone.
type resourceReader interface {
	ReadResources(ctx context.Context) (composemodel.ComposeProjectResources, error)
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

// Deps are Deployer's collaborators.
type Deps struct {
	ComposeRunner      composeRunner
	ContainerControl   containerResumer
	ContainerRebuilder containerRebuilder
	// Inspector reads the project by its label under its own step: the volumes a failed build
	// created, what a drop takes and leaves; ComposeReader reads it whole with none, to find the
	// containers a create built.
	Inspector      resourceReader
	ComposeReader  composeIntrospector
	VolumeRemover  volumeRemover
	ProjectRemover projectRemover
	// LayerTags are the restored layer tags a drop removes; LayerOrigin names the compose project
	// it takes down by name and whose tags those are.
	LayerTags   layerTags
	LayerOrigin composemodel.LayerOrigin
	// Concurrency caps the volumes restored at once; 0 leaves it unbounded.
	Concurrency int
}

// Deployer builds and runs one compose project.
type Deployer struct {
	composeRunner           composeRunner
	containerControl        containerResumer
	containerRebuilder      containerRebuilder
	inspector               resourceReader
	composeReader           composeIntrospector
	volumeRemover           volumeRemover
	projectRemover          projectRemover
	layerTags               layerTags
	layerOrigin             composemodel.LayerOrigin
	maxConcurrentOperations int
}

// New builds a Deployer from deps.
func New(deps Deps) *Deployer {
	return &Deployer{
		composeRunner:           deps.ComposeRunner,
		containerControl:        deps.ContainerControl,
		containerRebuilder:      deps.ContainerRebuilder,
		inspector:               deps.Inspector,
		composeReader:           deps.ComposeReader,
		volumeRemover:           deps.VolumeRemover,
		projectRemover:          deps.ProjectRemover,
		layerTags:               deps.LayerTags,
		layerOrigin:             deps.LayerOrigin,
		maxConcurrentOperations: fanout.Limit(deps.Concurrency),
	}
}

// Build creates the compose project from target's spec, its containers labelled with the
// deployment, and rebuilds every replica compose cannot name on its own layer and number. It
// returns the project as created: a labelled copy, target's own left as it is.
func (s *Deployer) Build(ctx context.Context, target composemodel.Target) (*types.Project, error) {
	var (
		recorded   = newRecordedReplicas(target.Savepoint, target.Project, target.Restored)
		deployment = newSavepointDeployment(target.Deployment, target.Project, recorded)
		project    = withDeploymentLabels(target.Project, composemodel.NewDeploymentLabels(deployment))
	)
	if err := s.composeCreate(ctx, project, newRestoredVolumeNames(target.Savepoint)); err != nil {
		return nil, err
	}
	if err := s.rebuildDivergentReplicas(ctx, recorded); err != nil {
		return nil, err
	}
	return project, nil
}

// Discard takes project down and drops the volumes it owns now and did not own before; it
// returns the down's error.
func (s *Deployer) Discard(ctx context.Context, project *types.Project, ownedBefore map[string]bool) error {
	downErr := s.Down(ctx, project)
	s.removeCreatedVolumes(ctx, ownedBefore)
	return downErr
}

// Reap drops the anonymous volumes a teardown orphaned and target's restore replaced, and says
// where the restored writable layers live.
func (s *Deployer) Reap(ctx context.Context, anonymousVolumeNames map[string]string, target composemodel.Target) {
	s.removeOrphanedVolumes(ctx, anonymousVolumeNames, composemodel.VolumesOf(target.Savepoint))
	logRestoredLayers(ctx, target.Restored)
}

// newRestoredVolumeNames are the docker names of the named volumes savepoint restores.
func newRestoredVolumeNames(savepoint composemodel.Savepoint) []string {
	var names []string
	for _, stat := range savepoint.Detail.Stats {
		if stat.Volume.Named != nil {
			names = append(names, stat.Volume.Named.VolumeName)
		}
	}
	return names
}

// composeCreate creates the containers of project without starting them: the data is
// restored into them first. Only a volume of restoredVolumes may be recreated.
func (s *Deployer) composeCreate(ctx context.Context, project *types.Project, restoredVolumes []string) error {
	if err := runComposeActivity(ctx, kindComposeCreate, func(ctx context.Context) error {
		return s.composeRunner.Create(ctx, project, restoredVolumes)
	}); err != nil {
		return fmt.Errorf("creating the compose project: %w", err)
	}
	return nil
}

// Start starts the containers of project and waits on their health checks.
func (s *Deployer) Start(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	return runComposeActivity(ctx, kindComposeStart, func(ctx context.Context) error {
		return s.composeRunner.Start(ctx, project, waitTimeout)
	})
}

// removeOrphanedVolumes removes the anonymous volumes `compose down` left behind whose data restored
// holds; any other is its own last copy.
func (s *Deployer) removeOrphanedVolumes(ctx context.Context, anonymousVolumeNames map[string]string, restored []composemodel.Volume) {
	log := zerolog.Ctx(ctx)
	for _, volume := range restored {
		name, ok := anonymousVolumeNames[volume.ObjectID]
		if !ok {
			continue
		}
		if err := s.volumeRemover.RemoveVolume(ctx, name); err != nil {
			log.Info().Err(err).Str("volume", name).Msg("orphaned anonymous volume left behind")
			progress.Current(ctx).Warn(fmt.Sprintf("orphaned anonymous volume %s left behind: %s", name, err))
			continue
		}
		log.Debug().Str("volume", name).Msg("removed the anonymous volume the teardown orphaned")
	}
}

// Down removes the compose project's containers and networks. It goes by project
// name, so it also takes down what a failed rebuild left standing.
func (s *Deployer) Down(ctx context.Context, project *types.Project) error {
	if err := runComposeActivity(ctx, kindComposeDown, func(ctx context.Context) error {
		return s.composeRunner.Down(ctx, project)
	}); err != nil {
		return fmt.Errorf("removing the compose project: %w", err)
	}
	return nil
}

// removeCreatedVolumes drops the volumes the compose project owns now and did not own
// before: what the failed rollback created, holding nothing the savepoint does not. The narrow
// read: a container left half-built must not keep the volumes from being found.
func (s *Deployer) removeCreatedVolumes(ctx context.Context, ownedBefore map[string]bool) {
	log := zerolog.Ctx(ctx)
	resources, err := s.inspector.ReadResources(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("cannot tell which volumes the failed rollback created; they are left behind")
		return
	}
	for _, name := range resources.Volumes {
		if ownedBefore[name] {
			continue
		}
		if err := s.volumeRemover.RemoveVolume(ctx, name); err != nil {
			log.Warn().Err(err).Str("volume", name).Msg("volume the failed rollback created left behind")
			continue
		}
		log.Debug().Str("volume", name).Msg("removed the volume the failed rollback created")
	}
}

// runComposeActivity runs fn as one compose activity of kind, a line per object compose reports.
func runComposeActivity(ctx context.Context, kind progress.Kind, fn func(ctx context.Context) error) error {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kind}, progress.MeasureResources)
	err := fn(ctx)
	step.End(err)
	return err
}

// logRestoredLayers says where the restored layers live: only in the containers the rollback
// built, which compose replaces on its own images at the next `up` of the file.
func logRestoredLayers(ctx context.Context, restored []composemodel.WritableLayerRestoreResult) {
	if len(restored) == 0 {
		return
	}
	zerolog.Ctx(ctx).Info().Int("writable_layers", len(restored)).
		Msg("the containers run on the savepoint's writable layers: `docker compose up` from your " +
			"compose file recreates them on its images and drops those layers; roll back again to restore them")
}

// UnpauseOnEntry resumes the replicas introspection found frozen, ahead of a teardown that does
// not; a failure is only logged, the teardown going on regardless.
func (s *Deployer) UnpauseOnEntry(ctx context.Context, introspection composemodel.IntrospectionResult) {
	var (
		ids   []string
		names []string
	)
	for _, svc := range introspection.Project.Services {
		for _, replica := range svc.Containers {
			if state := replica.Inspect.State; state != nil && state.Paused {
				ids = append(ids, replica.ID)
				names = append(names, fmt.Sprintf("%s-%d", svc.Name, replica.Number))
			}
		}
	}
	if len(ids) == 0 {
		return
	}

	step := progress.Current(ctx)
	for _, name := range names {
		step.Warn(name + " was paused: resuming it before the rollback")
	}
	if err := s.containerControl.UnpauseContainers(ctx, ids); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("cannot resume the paused containers; the rollback goes on")
	}
}
