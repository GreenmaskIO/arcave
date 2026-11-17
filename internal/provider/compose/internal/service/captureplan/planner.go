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
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/backendregistry"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
)

// errNoVolumesToBackUp refuses a savepoint that would cover nothing.
var errNoVolumesToBackUp = errors.New("no stateful volumes found; nothing to capture")

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

// Deps are Planner's collaborators.
type Deps struct {
	ProjectAssembler projectAssembler
	SpecCodec        specCodec
	BackendRegistry  model.SnapshotBackendRegistry
	// CaptureDefaults are the backends a unit no arcave.* label names is captured by.
	CaptureDefaults capturepolicy.CaptureDefaults
	// Store is the compose project's store, every backend's root.
	Store storages.Storager
}

// Planner decides the captures of one compose project.
type Planner struct {
	projectAssembler projectAssembler
	specCodec        specCodec
	backendRegistry  model.SnapshotBackendRegistry
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
	writableLayers []model.WritableLayerCaptureRequest
	// upstreams are the distinct images the captured services' compose file named.
	upstreams             []model.ImageCaptureRequest
	skippedWritableLayers []model.SkippedWritableLayer
	skippedImages         []model.SkippedImage
}

// Plan decides, off introspection, what a capture of the live compose project takes and leaves
// out; it changes nothing. restoring is the savepoint about to replace the project, nil for none. A
// capture policy that cannot be resolved is a provider.CapturePolicyError.
func (s *Planner) Plan(
	ctx context.Context, introspection model.IntrospectionResult, restoring *model.Savepoint,
) (composemodel.Plan, error) {
	var (
		capturePolicy model.CapturePolicy
		labelsIgnored error
		plan          composemodel.Plan
		err           error
	)
	if capturePolicy, labelsIgnored, err = s.resolveCapturePolicy(ctx, introspection, restoring); err != nil {
		return composemodel.Plan{}, &provider.CapturePolicyError{Err: err}
	}
	plan, err = s.newPlan(ctx, introspection, capturePolicy, labelsIgnored)
	if err != nil {
		// No plan carries labelsIgnored back, and following the config may be why it failed.
		warnLabelsIgnored(ctx, labelsIgnored)
		return composemodel.Plan{}, err
	}
	return plan, nil
}

// resolveCapturePolicy reads the compose project's labels into a policy; a project about to be
// replaced with restoring gets that replacement's policy, nil being none. The first error is why
// the policy follows the config and not the labels, nil when it reads them; the second is the
// failure.
func (s *Planner) resolveCapturePolicy(
	ctx context.Context, introspection model.IntrospectionResult, restoring *model.Savepoint,
) (model.CapturePolicy, error, error) {
	if restoring == nil {
		// Before anything else: a bad label fails while the project is still running.
		capturePolicy, err := capturepolicy.Resolve(ctx, introspection, s.captureDefaults)
		if err != nil {
			return model.CapturePolicy{}, nil, err
		}
		return capturePolicy, nil, nil
	}
	return s.newReplacementPolicy(ctx, introspection, *restoring)
}

// newReplacementPolicy is the live policy of a project about to be replaced with restoring, every
// unit it overwrites kept and every service stopped; a bad label falls back to the config and is
// returned as the first error, labelsIgnored, the failure being the second.
func (s *Planner) newReplacementPolicy(
	ctx context.Context, introspection model.IntrospectionResult, restoring model.Savepoint,
) (model.CapturePolicy, error, error) {
	var (
		capturePolicy model.CapturePolicy
		labelsIgnored error
		err           error
	)
	if capturePolicy, err = capturepolicy.Resolve(ctx, introspection, s.captureDefaults); err != nil {
		if !errors.Is(err, model.ErrInvalidCapturePolicy) {
			return model.CapturePolicy{}, nil, err
		}
		labelsIgnored = err
		if capturePolicy, err = capturepolicy.ResolveWithoutLabels(ctx, introspection, s.captureDefaults); err != nil {
			warnLabelsIgnored(ctx, labelsIgnored)
			return model.CapturePolicy{}, nil, err
		}
	}
	return withStopMode(keepRestored(capturePolicy, introspection, restoring)), labelsIgnored, nil
}

// warnLabelsIgnored says a capture that then failed had passed over a bad label: no plan is left
// to carry labelsIgnored back to the caller.
func warnLabelsIgnored(ctx context.Context, labelsIgnored error) {
	if labelsIgnored == nil {
		return
	}
	zerolog.Ctx(ctx).Warn().Err(labelsIgnored).Msg("the capture ignored the labels and followed the config")
}

// withStopMode stops every service, whatever its label or the config say: the project is about
// to be replaced, so its capture is held by stop alone.
func withStopMode(capturePolicy model.CapturePolicy) model.CapturePolicy {
	services := make(map[string]model.ServiceDecision, len(capturePolicy.Services))
	for name, decision := range capturePolicy.Services {
		decision.Mode = model.CaptureModeStop
		services[name] = decision
	}
	capturePolicy.Mode, capturePolicy.Services = model.CaptureModeStop, services
	return capturePolicy
}

// keepRestored captures every live unit savepoint restores into, so the capture holds what the
// restore overwrites.
func keepRestored(capturePolicy model.CapturePolicy, introspection model.IntrospectionResult, savepoint model.Savepoint) model.CapturePolicy {
	restored := map[string]bool{}
	for _, v := range composemodel.VolumesOf(savepoint) {
		restored[v.ObjectID] = true
	}
	keep := func(decision model.VolumeDecision) model.VolumeDecision {
		decision.CaptureDecision = model.NewConfigDecision(true, "the rollback restores into it")
		return decision
	}

	named := maps.Clone(capturePolicy.NamedVolumes)
	if named == nil {
		named = map[string]model.VolumeDecision{}
	}
	for _, volume := range introspection.Project.Volumes {
		if decision := capturePolicy.DecideNamedVolume(volume.Name); restored[volume.ObjectID()] && !decision.Capture {
			named[volume.Name] = keep(decision)
		}
	}
	mounts := maps.Clone(capturePolicy.Mounts)
	if mounts == nil {
		mounts = map[model.MountRef]model.VolumeDecision{}
	}
	for _, svc := range introspection.Project.Services {
		for _, mount := range svc.Volumes {
			ref := model.MountRef{Service: svc.Name, Replica: mount.Replica, Target: mount.Target}
			if decision := capturePolicy.DecideMount(ref); mount.Type != model.VolumeTypeNamed && restored[mount.ObjectID(svc.Name)] && !decision.Capture {
				mounts[ref] = keep(decision)
			}
		}
	}
	capturePolicy.NamedVolumes, capturePolicy.Mounts = named, mounts
	return capturePolicy
}

// newPlan renders and parses back the compose project's spec and builds one backend per volume
// capturePolicy takes; an absent compose project or an empty capture is refused.
func (s *Planner) newPlan(
	ctx context.Context, introspection model.IntrospectionResult, capturePolicy model.CapturePolicy, labelsIgnored error,
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

	backends, err := backendregistry.NewBackends(s.backendRegistry, s.store, volumes)
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
		NotCovered: model.NotCovered{
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
	introspection model.IntrospectionResult,
	capturePolicy model.CapturePolicy,
	containers []model.ContainerRef,
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
func newServiceHolds(introspection model.IntrospectionResult, capturePolicy model.CapturePolicy) []composemodel.ServiceHold {
	holds := make([]composemodel.ServiceHold, 0, len(introspection.Project.Services))
	for _, svc := range introspection.Project.Services {
		var (
			running []model.ContainerRef
			idle    []composemodel.IdleReplica
		)
		for _, replica := range svc.Containers {
			ref := model.ContainerRef{
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
		holds = append(holds, composemodel.ServiceHold{
			Service: svc.Name,
			Mode:    cmp.Or(capturePolicy.DecideService(svc.Name).Mode, model.CaptureModeStop),
			Running: running,
			Idle:    idle,
		})
	}
	return holds
}

// newReplicaGaps warns of and returns each service missing a replica below its highest one.
func newReplicaGaps(ctx context.Context, introspection model.IntrospectionResult) []model.ReplicaGap {
	var gaps []model.ReplicaGap
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
		gaps = append(gaps, model.ReplicaGap{
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
func requireComposeProject(introspection model.IntrospectionResult) error {
	if composemodel.IsAbsent(introspection) {
		return fmt.Errorf("compose project %q: %w", introspection.Project.Name, model.ErrComposeProjectAbsent)
	}
	return nil
}
