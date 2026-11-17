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
	"errors"
	"fmt"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/sessionstore"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// errForeignPrepared refuses a prepared savepoint another provider handed out.
var errForeignPrepared = errors.New("the savepoint was prepared by another provider")

// preparedSavepoint is a savepoint ready to build, as this provider hands it to core.
type preparedSavepoint struct {
	composemodel.Target
}

// Summary is what building the prepared savepoint restores, and the volumes it overwrites.
func (p preparedSavepoint) Summary() provider.PreparedSummary {
	return provider.PreparedSummary{
		Volumes:        composemodel.VolumesOf(p.Savepoint),
		WritableLayers: p.Restored,
		Overwritten:    p.Overwritten,
	}
}

// PrepareSavepoint makes the savepoint id names ready to build. Over an absent compose project, a
// leftover volume it restores into is refused or named as overwritten, as the policy says.
func (l liveWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	savepoint, err := sessionstore.LoadSavepoint(ctx, l.service.savepointRepo, id)
	if err != nil {
		return nil, err
	}
	var overwritten []string
	if l.Absent() {
		if overwritten, err = l.service.preparer.NewOverwritten(l.introspection, savepoint, opts.Recovering); err != nil {
			return nil, err
		}
	}
	target, err := l.service.preparer.Prepare(ctx, savepoint, opts.Deployment, overwritten)
	if err != nil {
		return nil, err
	}
	return preparedSavepoint{Target: target}, nil
}

// Teardown resumes the replicas found frozen and runs `compose down` of prepared's project; over
// an absent compose project it removes nothing.
func (l liveWorkload) Teardown(ctx context.Context, prepared provider.PreparedSavepoint) (provider.VacantWorkload, error) {
	target, err := toPreparedSavepoint(prepared)
	if err != nil {
		return nil, err
	}
	if !l.Absent() {
		l.service.unpauseOnEntry(ctx, l.introspection)
		if err := l.service.composeDown(ctx, target.Project); err != nil {
			return nil, err
		}
	}
	return vacantWorkload{
		service:              l.service,
		anonymousVolumeNames: composemodel.NewAnonymousVolumeNames(l.introspection),
		ownedBefore:          composemodel.OwnedVolumes(l.introspection),
		downProject:          target.Project,
	}, nil
}

// unpauseOnEntry resumes the replicas introspection found frozen, ahead of a teardown that does
// not; a failure is only logged, the teardown going on regardless.
func (s *Provider) unpauseOnEntry(ctx context.Context, introspection model.IntrospectionResult) {
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

// vacantWorkload is the compose project with nothing of it running: the anonymous volumes its teardown
// orphaned, the volumes it owned before the build, and the project a discard takes down.
type vacantWorkload struct {
	service              *Provider
	anonymousVolumeNames map[string]string
	ownedBefore          map[string]bool
	downProject          *types.Project
}

// Build creates the compose project from the prepared savepoint's spec, its containers labelled
// with the deployment, and rebuilds every replica compose cannot name on its own layer and number.
func (v vacantWorkload) Build(ctx context.Context, prepared provider.PreparedSavepoint) (provider.BuiltWorkload, error) {
	target, err := toPreparedSavepoint(prepared)
	if err != nil {
		return nil, err
	}
	var (
		recorded   = newRecordedReplicas(target.Savepoint, target.Project, target.Restored)
		deployment = newSavepointDeployment(target.Deployment, target.Project, recorded)
		// A copy: the project the caller holds is the one a revert or a discard acts on later.
		project = withDeploymentLabels(target.Project, deployment.Labels())
	)
	if err := v.service.composeCreate(ctx, project, newRestoredVolumeNames(target.Savepoint)); err != nil {
		return nil, err
	}
	if err := v.service.rebuildDivergentReplicas(ctx, recorded); err != nil {
		return nil, err
	}
	return builtWorkload{
		vacant:  v,
		target:  target,
		project: project,
	}, nil
}

// Discard runs `compose down` of the project and drops the volumes it owns now and did not own
// before; it returns the down's error.
func (v vacantWorkload) Discard(ctx context.Context) error {
	downErr := v.service.composeDown(ctx, v.downProject)
	v.service.removeCreatedVolumes(ctx, v.ownedBefore)
	return downErr
}

// builtWorkload is the compose project created from the prepared savepoint, labelled, and empty.
type builtWorkload struct {
	vacant  vacantWorkload
	target  preparedSavepoint
	project *types.Project
}

// RestoreData writes every volume of the savepoint back.
func (b builtWorkload) RestoreData(ctx context.Context) (provider.RestoredWorkload, error) {
	if err := b.vacant.service.restoreVolumes(ctx, b.target.Savepoint, b.target.Backends); err != nil {
		return nil, err
	}
	return restoredWorkload(b), nil
}

// restoredWorkload is the compose project holding the savepoint's data, not running.
type restoredWorkload struct {
	vacant  vacantWorkload
	target  preparedSavepoint
	project *types.Project
}

// Start starts the services and waits on their health checks.
func (r restoredWorkload) Start(ctx context.Context, waitTimeout time.Duration) error {
	return r.vacant.service.composeStart(ctx, r.project, waitTimeout)
}

// Reap drops the anonymous volumes the teardown orphaned and the restore replaced, and says where
// the restored writable layers live.
func (r restoredWorkload) Reap(ctx context.Context) {
	r.vacant.service.removeOrphanedVolumes(ctx, r.vacant.anonymousVolumeNames, composemodel.VolumesOf(r.target.Savepoint))
	logRestoredLayers(ctx, r.target.Restored)
}

// toPreparedSavepoint is prepared as this provider prepared it.
func toPreparedSavepoint(prepared provider.PreparedSavepoint) (preparedSavepoint, error) {
	target, ok := prepared.(preparedSavepoint)
	if !ok {
		return preparedSavepoint{}, errForeignPrepared
	}
	return target, nil
}

// newRestoredVolumeNames are the docker names of the named volumes savepoint restores.
func newRestoredVolumeNames(savepoint model.Savepoint) []string {
	var names []string
	for _, stat := range savepoint.Stats {
		if stat.Volume.Named != nil {
			names = append(names, stat.Volume.Named.VolumeName)
		}
	}
	return names
}

// composeCreate creates the containers of project without starting them: the data is
// restored into them first. Only a volume of restoredVolumes may be recreated.
func (s *Provider) composeCreate(ctx context.Context, project *types.Project, restoredVolumes []string) error {
	if err := runComposeActivity(ctx, kindComposeCreate, func(ctx context.Context) error {
		return s.composeRunner.Create(ctx, project, restoredVolumes)
	}); err != nil {
		return fmt.Errorf("creating the compose project: %w", err)
	}
	return nil
}

// composeStart starts the containers of project and waits on their health checks.
func (s *Provider) composeStart(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	return runComposeActivity(ctx, kindComposeStart, func(ctx context.Context) error {
		return s.composeRunner.Start(ctx, project, waitTimeout)
	})
}

// removeOrphanedVolumes removes the anonymous volumes `compose down` left behind whose data restored
// holds; any other is its own last copy.
func (s *Provider) removeOrphanedVolumes(ctx context.Context, anonymousVolumeNames map[string]string, restored []model.Volume) {
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

// composeDown removes the compose project's containers and networks. It goes by project
// name, so it also takes down what a failed rebuild left standing.
func (s *Provider) composeDown(ctx context.Context, project *types.Project) error {
	if err := runComposeActivity(ctx, kindComposeDown, func(ctx context.Context) error {
		return s.composeRunner.Down(ctx, project)
	}); err != nil {
		return fmt.Errorf("removing the compose project: %w", err)
	}
	return nil
}

// removeCreatedVolumes drops the volumes the compose project owns now and did not own
// before: what the failed rollback created, holding nothing the savepoint does not.
func (s *Provider) removeCreatedVolumes(ctx context.Context, ownedBefore map[string]bool) {
	log := zerolog.Ctx(ctx)
	introspection, err := s.inspector.Introspect(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("cannot tell which volumes the failed rollback created; they are left behind")
		return
	}
	for name := range composemodel.OwnedVolumes(introspection) {
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
func logRestoredLayers(ctx context.Context, restored []model.WritableLayerRestoreResult) {
	if len(restored) == 0 {
		return
	}
	zerolog.Ctx(ctx).Info().Int("writable_layers", len(restored)).
		Msg("the containers run on the savepoint's writable layers: `docker compose up` from your " +
			"compose file recreates them on its images and drops those layers; roll back again to restore them")
}
