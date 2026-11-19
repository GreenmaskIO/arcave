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

// Package captureplan decides one capture of a compose project before anything is touched: what
// it takes, by which backends, and what it leaves out.
package captureplan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/volumebackend"
)

// errNoVolumesToBackUp refuses a savepoint that would cover nothing.
var errNoVolumesToBackUp = errors.New("no stateful volumes found; nothing to capture")

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

// Deps are Planner's collaborators.
type Deps struct {
	ProjectAssembler projectAssembler
	SpecCodec        specCodec
	BackendRegistry  composemodel.SnapshotBackendRegistry
	// CaptureDefaults are the backends a unit no arcave.* label names is captured by.
	CaptureDefaults capturepolicy.CaptureDefaults
	// Store is the compose project's store, every backend's root.
	Store storages.Storager
}

// Planner decides the captures of one compose project.
type Planner struct {
	projectAssembler projectAssembler
	specCodec        specCodec
	backendRegistry  composemodel.SnapshotBackendRegistry
	captureDefaults  capturepolicy.CaptureDefaults
	store            storages.Storager
}

// New builds a Planner from deps.
func New(deps Deps) *Planner {
	return &Planner{
		projectAssembler: deps.ProjectAssembler,
		specCodec:        deps.SpecCodec,
		backendRegistry:  deps.BackendRegistry,
		captureDefaults:  deps.CaptureDefaults,
		store:            deps.Store,
	}
}

// imageSet is what a capture takes of the images, as the policy decides.
type imageSet struct {
	// writableLayers are the replicas whose writable layer a capture commits, each with
	// its backend; the savepoint id is filled in at the capture.
	writableLayers []composemodel.WritableLayerCaptureRequest
	// upstreams are the distinct images the captured services' compose file named.
	upstreams             []composemodel.ImageCaptureRequest
	skippedWritableLayers []composemodel.SkippedWritableLayer
	skippedImages         []composemodel.SkippedImage
}

// Plan decides, off introspection, what a capture of the live compose project takes and leaves
// out; it changes nothing. A project about to be replaced with restoring, nil for none, gets that
// replacement's policy, which follows the config past a bad label; any other capture refuses one, a
// provider.CapturePolicyError.
func (s *Planner) Plan(
	ctx context.Context, introspection composemodel.IntrospectionResult, restoring *composemodel.Savepoint,
) (composemodel.Plan, error) {
	var (
		capturePolicy composemodel.CapturePolicy
		labelsIgnored error
		plan          composemodel.Plan
		err           error
	)
	if restoring == nil {
		// Before anything else: a bad label fails while the project is still running.
		if capturePolicy, err = capturepolicy.Resolve(ctx, introspection, s.captureDefaults); err != nil {
			return composemodel.Plan{}, &provider.CapturePolicyError{Err: err}
		}
	} else {
		capturePolicy, labelsIgnored = s.newReplacementPolicy(ctx, introspection, *restoring)
	}
	plan, err = s.newPlan(ctx, introspection, capturePolicy, labelsIgnored)
	if err != nil {
		// No plan carries labelsIgnored back, and following the config may be why it failed.
		warnLabelsIgnored(ctx, labelsIgnored)
		return composemodel.Plan{}, err
	}
	return plan, nil
}

// newReplacementPolicy is the live policy of a project about to be replaced with restoring: the
// labels' decisions, every unit the restore overwrites kept, every service stopped and no hook run.
// A label it cannot read leaves the decisions to the config and is returned, labelsIgnored.
func (s *Planner) newReplacementPolicy(
	ctx context.Context, introspection composemodel.IntrospectionResult, restoring composemodel.Savepoint,
) (composemodel.CapturePolicy, error) {
	labelled, labelsIgnored := capturepolicy.Resolve(ctx, introspection, s.captureDefaults)
	if labelsIgnored != nil {
		labelled = capturepolicy.ResolveWithoutLabels(introspection, s.captureDefaults)
	}
	restored := newRestoredObjectIDs(restoring)
	return composemodel.CapturePolicy{
		VolumeBackend:        labelled.VolumeBackend,
		WritableLayerBackend: labelled.WritableLayerBackend,
		Mode:                 composeconfig.CaptureModeStop,
		Services:             newStoppedServices(labelled.Services),
		NamedVolumes:         newKeptNamedVolumes(labelled, introspection, restored),
		Mounts:               newKeptMounts(labelled, introspection, restored),
	}, labelsIgnored
}

// warnLabelsIgnored says a capture that then failed had passed over a bad label: no plan is left
// to carry labelsIgnored back to the caller.
func warnLabelsIgnored(ctx context.Context, labelsIgnored error) {
	if labelsIgnored == nil {
		return
	}
	zerolog.Ctx(ctx).Warn().Err(labelsIgnored).Msg("the capture ignored the labels and followed the config")
}

// newRestoredObjectIDs are the volumes savepoint restores into, by object id.
func newRestoredObjectIDs(savepoint composemodel.Savepoint) map[string]bool {
	restored := map[string]bool{}
	for _, volume := range composemodel.VolumesOf(savepoint) {
		restored[volume.ObjectID] = true
	}
	return restored
}

// newStoppedServices are the services' decisions held by stop and with no hook, whatever their
// labels or the config say: the project is about to be replaced.
func newStoppedServices(decisions map[string]composemodel.ServiceDecision) map[string]composemodel.ServiceDecision {
	services := make(map[string]composemodel.ServiceDecision, len(decisions))
	for name, decision := range decisions {
		decision.Mode = composeconfig.CaptureModeStop
		decision.PreCapture = nil
		decision.PostCapture = nil
		services[name] = decision
	}
	return services
}

// newKeptNamedVolumes are capturePolicy's named volume decisions, each one left out that the restore
// overwrites taken, so the capture holds what the restore destroys.
func newKeptNamedVolumes(
	capturePolicy composemodel.CapturePolicy, introspection composemodel.IntrospectionResult, restored map[string]bool,
) map[string]composemodel.VolumeDecision {
	named := maps.Clone(capturePolicy.NamedVolumes)
	if named == nil {
		named = map[string]composemodel.VolumeDecision{}
	}
	for _, volume := range introspection.Project.Volumes {
		if decision := capturePolicy.DecideNamedVolume(volume.Name); restored[volume.ObjectID()] && !decision.Capture {
			named[volume.Name] = newKeptDecision(decision)
		}
	}
	return named
}

// newKeptMounts are capturePolicy's bind and anonymous volume decisions, each one left out that the
// restore overwrites taken.
func newKeptMounts(
	capturePolicy composemodel.CapturePolicy, introspection composemodel.IntrospectionResult, restored map[string]bool,
) map[composemodel.MountRef]composemodel.VolumeDecision {
	mounts := maps.Clone(capturePolicy.Mounts)
	if mounts == nil {
		mounts = map[composemodel.MountRef]composemodel.VolumeDecision{}
	}
	for _, svc := range introspection.Project.Services {
		for _, mount := range svc.Volumes {
			ref := composemodel.MountRef{Service: svc.Name, Replica: mount.Replica, Target: mount.Target}
			decision := capturePolicy.DecideMount(ref)
			if mount.Type != composemodel.VolumeTypeNamed && restored[mount.ObjectID(svc.Name)] && !decision.Capture {
				mounts[ref] = newKeptDecision(decision)
			}
		}
	}
	return mounts
}

// newKeptDecision is decision taken because the rollback restores into it, on its backend.
func newKeptDecision(decision composemodel.VolumeDecision) composemodel.VolumeDecision {
	return composemodel.VolumeDecision{
		CaptureDecision: composemodel.NewConfigDecision(true, "the rollback restores into it"),
		Backend:         decision.Backend,
	}
}

// newPlan renders and parses back the compose project's spec and builds one backend per volume
// capturePolicy takes; an absent compose project or an empty capture is refused.
func (s *Planner) newPlan(
	ctx context.Context, introspection composemodel.IntrospectionResult, capturePolicy composemodel.CapturePolicy, labelsIgnored error,
) (composemodel.Plan, error) {
	if err := requireComposeProject(introspection); err != nil {
		return composemodel.Plan{}, err
	}

	containers := newContainerRefs(ctx, introspection)
	images := newImageSet(ctx, introspection, capturePolicy, containers)

	// Before the stop, so a spec that cannot be rebuilt leaves the project running.
	assembled, err := s.projectAssembler.Assemble(ctx, introspection)
	if err != nil {
		return composemodel.Plan{}, fmt.Errorf("assembling the compose spec: %w", err)
	}
	// The spec says what this capture takes and why, so the savepoint explains itself.
	stampServices(assembled, capturepolicy.Stamps(introspection, capturePolicy))
	spec, err := s.specCodec.Marshal(assembled)
	if err != nil {
		return composemodel.Plan{}, fmt.Errorf("rendering the compose spec: %w", err)
	}
	// Through the loader, as a rollback reads the artifact: the live compose project is
	// acted on as these very bytes describe it.
	project, err := s.specCodec.Parse(ctx, spec)
	if err != nil {
		return composemodel.Plan{}, fmt.Errorf("parsing the compose spec: %w", err)
	}

	volumes, notCovered, err := resolveBackupVolumes(ctx, introspection, capturePolicy)
	if err != nil {
		return composemodel.Plan{}, err
	}
	if len(volumes) == 0 {
		return composemodel.Plan{}, errNoVolumesToBackUp
	}

	backends, err := volumebackend.NewBackends(s.backendRegistry, s.store, volumes)
	if err != nil {
		return composemodel.Plan{}, err
	}

	return composemodel.Plan{
		Project:        project,
		Spec:           spec,
		Volumes:        backends,
		Images:         images.upstreams,
		WritableLayers: images.writableLayers,
		Services:       newServiceHolds(introspection, capturePolicy),
		Containers:     containers,
		NotCovered: composemodel.NotCovered{
			Mounts:                 notCovered.Mounts,
			ServicesWithoutVolumes: notCovered.ServicesWithoutVolumes,
			ReplicaGaps:            newReplicaGaps(ctx, introspection),
			UnresolvedImages:       newUnresolvedImages(introspection, containers),
			WritableLayers:         images.skippedWritableLayers,
			Images:                 images.skippedImages,
		},
		LabelsIgnored: labelsIgnored,
	}, nil
}

// newImageSet decides the writable layers and images a capture takes.
func newImageSet(
	ctx context.Context,
	introspection composemodel.IntrospectionResult,
	capturePolicy composemodel.CapturePolicy,
	containers []composemodel.ContainerRef,
) imageSet {
	writableLayers, skippedWritableLayers := newLayerCaptures(ctx, containers, capturePolicy)
	upstreamContainers, skippedImages := imageCaptureContainers(ctx, containers, capturePolicy)
	return imageSet{
		writableLayers:        writableLayers,
		upstreams:             newImageCaptures(introspection, upstreamContainers),
		skippedWritableLayers: skippedWritableLayers,
		skippedImages:         skippedImages,
	}
}

// stampServices adds labels to the services of project they name.
func stampServices(project *types.Project, labels map[string]map[string]string) {
	for name, stamp := range labels {
		svc, ok := project.Services[name]
		if !ok {
			continue
		}
		svc.Labels = maps.Clone(svc.Labels)
		if svc.Labels == nil {
			svc.Labels = types.Labels{}
		}
		maps.Copy(svc.Labels, stamp)
		project.Services[name] = svc
	}
}

// newServiceHolds decides how each service is held still and sorts its replicas by what the daemon
// reported of them: running, or already still.
func newServiceHolds(introspection composemodel.IntrospectionResult, capturePolicy composemodel.CapturePolicy) []composemodel.ServiceHold {
	holds := make([]composemodel.ServiceHold, 0, len(introspection.Project.Services))
	for _, svc := range introspection.Project.Services {
		var (
			running []composemodel.ContainerRef
			idle    []composemodel.IdleReplica
		)
		for _, replica := range svc.Containers {
			ref := composemodel.ContainerRef{
				Service: svc.Name,
				Replica: replica.Number,
				ID:      replica.ID,
				Name:    replica.Name,
			}
			state := replica.Inspect.State
			// No state is never the daemon's answer; it is read as running.
			if state == nil || state.Running && !state.Paused && !state.Restarting {
				running = append(running, ref)
				continue
			}
			idle = append(idle, composemodel.IdleReplica{
				Ref:   ref,
				State: string(state.Status),
			})
		}
		decision := capturePolicy.DecideService(svc.Name)
		holds = append(holds, composemodel.ServiceHold{
			Service:     svc.Name,
			Mode:        cmp.Or(decision.Mode, composeconfig.CaptureModeStop),
			Running:     running,
			Idle:        idle,
			PreCapture:  decision.PreCapture,
			PostCapture: decision.PostCapture,
		})
	}
	return holds
}

// newReplicaGaps warns of and returns each service missing a replica below its highest one.
func newReplicaGaps(ctx context.Context, introspection composemodel.IntrospectionResult) []composemodel.ReplicaGap {
	var gaps []composemodel.ReplicaGap
	for _, svc := range introspection.Project.Services {
		numbers := make([]int, 0, len(svc.Containers))
		for _, container := range svc.Containers {
			numbers = append(numbers, container.Number)
		}
		missing := missingReplicas(numbers)
		if len(missing) == 0 {
			continue
		}
		slices.Sort(numbers)
		zerolog.Ctx(ctx).Warn().Str("service", svc.Name).Ints("replicas", numbers).Ints("missing", missing).
			Msg("some replicas of this service are gone (removed, or exited and cleaned up); " +
				"only the remaining ones are captured, and a rollback brings back exactly these, under the same names")
		gaps = append(gaps, composemodel.ReplicaGap{
			Service:  svc.Name,
			Replicas: numbers,
			Missing:  missing,
		})
	}
	return gaps
}

// missingReplicas is the numbers from 1 up to the highest of numbers that no replica holds.
func missingReplicas(numbers []int) []int {
	if len(numbers) == 0 {
		return nil
	}
	var missing []int
	for n := 1; n < slices.Max(numbers); n++ {
		if !slices.Contains(numbers, n) {
			missing = append(missing, n)
		}
	}
	return missing
}

// requireComposeProject refuses a compose project the daemon holds no container of: there is
// nothing to assemble a spec from.
func requireComposeProject(introspection composemodel.IntrospectionResult) error {
	if composemodel.IsAbsent(introspection) {
		return fmt.Errorf("compose project %q: %w", introspection.Project.Name, model.ErrComposeProjectAbsent)
	}
	return nil
}
