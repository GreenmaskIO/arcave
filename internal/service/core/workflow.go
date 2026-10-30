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
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// workingSet is what a mutating workflow operates on: the introspection taken once
// before the stop, its rendered spec and the project parsed from it, the request's
// bound, and one backend per volume.
type workingSet struct {
	project     *types.Project
	spec        []byte
	waitTimeout time.Duration
	backends    []model.VolumeSnapshotBackend
	// containers are every replica of the compose project.
	containers []model.ContainerRef
	// images are the layers and upstreams the capture takes.
	images imageSet
	// anonVolumes maps a mount's ObjectID to the docker name behind it — the only
	// record of what a teardown orphans.
	anonVolumes map[string]string
	// What a capture of this set leaves out, as known before the stop.
	skippedMounts          []model.SkippedVolume
	servicesWithoutVolumes []string
	replicaGaps            []model.ReplicaGap
	unresolvedImages       []model.UnresolvedImage
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

// newNotCovered is what a capture of ws leaves out, killed being the containers the
// stop killed; nil when it leaves out nothing.
func (ws workingSet) newNotCovered(killed []string) *model.NotCovered {
	notCovered := model.NotCovered{
		Mounts:                 ws.skippedMounts,
		ServicesWithoutVolumes: ws.servicesWithoutVolumes,
		KilledContainers:       ws.newKilledContainers(killed),
		ReplicaGaps:            ws.replicaGaps,
		UnresolvedImages:       ws.unresolvedImages,
		WritableLayers:         ws.images.skippedWritableLayers,
		Images:                 ws.images.skippedImages,
	}
	if notCovered.IsEmpty() {
		return nil
	}
	return &notCovered
}

// newKilledContainers names the replica behind each container id the stop killed.
func (ws workingSet) newKilledContainers(ids []string) []model.KilledContainer {
	var killedContainers []model.KilledContainer
	for _, id := range ids {
		killed := model.KilledContainer{Container: id}
		if i := slices.IndexFunc(ws.containers, func(c model.ContainerRef) bool { return c.ID == id }); i >= 0 {
			ref := ws.containers[i]
			killed = model.KilledContainer{
				Service:   ref.Service,
				Replica:   ref.Replica,
				Container: ref.Name,
			}
		}
		killedContainers = append(killedContainers, killed)
	}
	return killedContainers
}

// prepare introspects the compose project and builds the working set of it.
func (s *Service) prepare(ctx context.Context, waitTimeout time.Duration) (workingSet, error) {
	introspection, err := s.introspect(ctx)
	if err != nil {
		return workingSet{}, err
	}
	if err := requireDaemon(ctx, introspection.Daemon, s.minDaemonMajor); err != nil {
		return workingSet{}, err
	}
	// Before anything else: a bad label fails while the project is still running.
	capturePolicy, err := capturepolicy.Resolve(ctx, introspection, s.captureDefaults)
	if err != nil {
		return workingSet{}, err
	}
	return s.newWorkingSet(ctx, introspection, waitTimeout, capturePolicy)
}

// introspect reads the compose project as the daemon holds it now.
func (s *Service) introspect(ctx context.Context) (model.IntrospectionResult, error) {
	ctx, node := progress.Run(ctx, progress.Step{Kind: kindProjectIntrospect})
	introspection, err := s.composeInspector.Introspect(ctx)
	node.End(err)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("introspect compose project: %w", err)
	}
	return introspection, nil
}

// newWorkingSet renders and parses back the compose project's spec and builds one backend
// per live volume, capturing what capturePolicy takes; an absent compose project or an empty
// capture is refused.
func (s *Service) newWorkingSet(
	ctx context.Context, introspection model.IntrospectionResult, waitTimeout time.Duration, capturePolicy model.CapturePolicy,
) (workingSet, error) {
	if err := requireComposeProject(introspection); err != nil {
		return workingSet{}, err
	}

	containers := newContainerRefs(ctx, introspection)
	images := newImageSet(ctx, introspection, capturePolicy, containers)

	// Before the stop, so a spec that cannot be rebuilt leaves the project running.
	assembled, err := s.projectAssembler.Assemble(ctx, introspection)
	if err != nil {
		return workingSet{}, fmt.Errorf("assembling the compose spec: %w", err)
	}
	// The spec says what this capture takes and why, so the savepoint explains itself.
	stampServices(assembled, capturepolicy.Stamps(introspection, capturePolicy))
	spec, err := s.specCodec.Marshal(assembled)
	if err != nil {
		return workingSet{}, fmt.Errorf("rendering the compose spec: %w", err)
	}
	// Through the loader, as a rollback reads the artifact: the live compose project is
	// acted on as these very bytes describe it.
	project, err := s.specCodec.Parse(ctx, spec)
	if err != nil {
		return workingSet{}, fmt.Errorf("parsing the compose spec: %w", err)
	}

	volumes, notCovered, err := s.resolveBackupVolumes(ctx, introspection, capturePolicy)
	if err != nil {
		return workingSet{}, err
	}
	if len(volumes) == 0 {
		return workingSet{}, errNoVolumesToBackUp
	}

	backends, err := s.newBackends(volumes)
	if err != nil {
		return workingSet{}, err
	}

	return workingSet{
		project:     project,
		spec:        spec,
		waitTimeout: waitTimeout,
		backends:    backends,
		containers:  containers,
		images:      images,
		anonVolumes: anonymousVolumes(introspection),

		skippedMounts:          notCovered.Mounts,
		servicesWithoutVolumes: notCovered.ServicesWithoutVolumes,
		replicaGaps:            newReplicaGaps(ctx, introspection),
		unresolvedImages:       newUnresolvedImages(introspection, containers),
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

// newSafetySnapshotPolicy is the capture policy of the safety snapshot taken before a
// rollback to restoring: the live labels, as a savepoint reads them, with two exceptions.
// A label that cannot be parsed warns and falls back to the config instead of failing
// the rollback, and every volume the rollback overwrites is captured whatever its label
// says, since the safety snapshot is the only copy of it.
func (s *Service) newSafetySnapshotPolicy(
	ctx context.Context, introspection model.IntrospectionResult, restoring model.Savepoint,
) (model.CapturePolicy, error) {
	capturePolicy, err := capturepolicy.Resolve(ctx, introspection, s.captureDefaults)
	if err != nil {
		if !errors.Is(err, model.ErrInvalidCapturePolicy) {
			return model.CapturePolicy{}, err
		}
		zerolog.Ctx(ctx).Warn().Err(err).Msg("the safety snapshot ignores the labels and follows the config")
		if capturePolicy, err = capturepolicy.ResolveWithoutLabels(ctx, introspection, s.captureDefaults); err != nil {
			return model.CapturePolicy{}, err
		}
	}
	return keepRestored(capturePolicy, introspection, restoring), nil
}

// keepRestored captures every live unit savepoint restores into, so the safety snapshot
// holds what the rollback overwrites.
func keepRestored(capturePolicy model.CapturePolicy, introspection model.IntrospectionResult, savepoint model.Savepoint) model.CapturePolicy {
	restored := map[string]bool{}
	for _, v := range volumesOf(savepoint) {
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

// composeProjectAbsent reports whether the daemon holds no container of the compose project:
// it was never started, or was taken down.
func composeProjectAbsent(introspection model.IntrospectionResult) bool {
	return len(introspection.Project.Services) == 0
}

// stopServices puts the services down and returns the containers the stop deadline killed, which
// the capture's not-covered warning reports.
func (s *Service) stopServices(ctx context.Context, working workingSet) ([]string, error) {
	ctx, node := progress.Run(ctx, progress.Step{Kind: kindComposeStop}, progress.MeasureResources)
	killed, err := s.composeRunner.Stop(ctx, working.project)
	node.End(err)
	if err != nil {
		return nil, fmt.Errorf("stopping services: %w", err)
	}
	return killed, nil
}

// composeDown removes the compose project's containers and networks. It goes by project
// name, so it also takes down what a failed rebuild left standing.
func (s *Service) composeDown(ctx context.Context, project *types.Project) error {
	if err := runComposeActivity(ctx, kindComposeDown, func(ctx context.Context) error {
		return s.composeRunner.Down(ctx, project)
	}); err != nil {
		return fmt.Errorf("removing the compose project: %w", err)
	}
	return nil
}

// composeCreate creates the containers of project without starting them: the data is
// restored into them first.
func (s *Service) composeCreate(ctx context.Context, project *types.Project) error {
	if err := runComposeActivity(ctx, kindComposeCreate, func(ctx context.Context) error {
		return s.composeRunner.Create(ctx, project)
	}); err != nil {
		return fmt.Errorf("creating the compose project: %w", err)
	}
	return nil
}

// startServices brings the services back and waits on their health checks.
func (s *Service) startServices(ctx context.Context, working workingSet) error {
	if err := s.composeStart(ctx, working.project, working.waitTimeout); err != nil {
		return fmt.Errorf("starting services: %w", err)
	}
	return nil
}

// composeStart starts the containers of project and waits on their health checks.
func (s *Service) composeStart(ctx context.Context, project *types.Project, waitTimeout time.Duration) error {
	return runComposeActivity(ctx, kindComposeStart, func(ctx context.Context) error {
		return s.composeRunner.Start(ctx, project, waitTimeout)
	})
}

// restartServices brings back the services a workflow stopped, on a revertContext:
// the interrupt that aborted the workflow must not leave the compose project stopped.
func (s *Service) restartServices(ctx context.Context, working workingSet) error {
	ctx, cancel := revertContext(ctx)
	defer cancel()
	return s.startServices(ctx, working)
}

// revertContext strips ctx of its cancellation and bounds it by revertBudget instead, for a
// step that puts the environment back and so must outlive a Ctrl-C.
func revertContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), revertBudget)
}

// logInterrupt tells the user what arcave does about an interrupt, when ctx was interrupted.
func logInterrupt(ctx context.Context, action string) {
	if ctx.Err() == nil {
		return
	}
	zerolog.Ctx(ctx).Info().Msg(action)
}

// preferInterrupt returns the interrupt that cancelled ctx in place of err, the symptom
// it caused somewhere downstream, and logs err.
func preferInterrupt(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	zerolog.Ctx(ctx).Debug().Err(err).Msg("failure the interrupt caused")
	// The cause, not ctx.Err: a lost session lock interrupts too, and says so.
	return fmt.Errorf("interrupted: %w", context.Cause(ctx))
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

// anonymousVolumes indexes the docker names of the compose project's anonymous volumes by
// the identity their data is captured under.
func anonymousVolumes(introspection model.IntrospectionResult) map[string]string {
	names := map[string]string{}
	for _, svc := range introspection.Project.Services {
		for _, mount := range svc.Volumes {
			if mount.Type == model.VolumeTypeAnon && mount.VolumeName != "" {
				names[mount.ObjectID(svc.Name)] = mount.VolumeName
			}
		}
	}
	return names
}
