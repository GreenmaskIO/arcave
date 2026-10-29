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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/pkg/progress"

	"github.com/greenmaskio/arcave/internal/model"
)

// errImageUnavailable refuses a rollback that would rebuild a service on an image
// neither the savepoint nor the daemon holds, and compose may not pull.
var errImageUnavailable = errors.New("image is neither in the savepoint nor in the daemon")

// maxRepositoryPath is docker's own cap on a repository path, the registry domain
// aside — RepositoryNameTotalLengthMax of github.com/distribution/reference.
const maxRepositoryPath = 255

// replicaPair is one recorded replica and the replica compose created in its place.
type replicaPair struct {
	recorded int
	created  int
}

// recordedReplica is one replica a savepoint recorded and the image a rollback builds
// it on: its restored layer's tag, or its service's own image.
type recordedReplica struct {
	service       string
	number        int
	containerName string
	image         string
}

// replicaRebuild is one recorded replica and the created container it is rebuilt over.
type replicaRebuild struct {
	recorded    recordedReplica
	created     int
	containerID string
}

// captureWritableLayers commits every requested writable layer under one savepoint id,
// concurrently, and returns one capture per request in input order.
func (s *Service) captureWritableLayers(
	ctx context.Context, snapshotID model.SavepointID, reqs []model.WritableLayerCaptureRequest,
) ([]model.WritableLayer, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	if err := snapshotID.Validate(); err != nil {
		return nil, fmt.Errorf("validate savepoint id: %w", err)
	}

	captures := make([]model.WritableLayer, len(reqs))
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindLayerCapture}, len(reqs))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, req := range reqs {
		eg.Go(func() error {
			container := req.Container
			req.SnapshotID = snapshotID
			opCtx := imageOpContext(egCtx, container, snapshotID)
			opCtx, node := progress.Run(opCtx, progress.Step{Kind: kindLayer, ID: container.Name}, progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("writable layer capture started")
			capture, err := s.imageBackend.CaptureWritableLayer(opCtx, req)
			if err != nil {
				err = fmt.Errorf("capture writable layer of %s/%d: %w", container.Service, container.Replica, err)
				node.End(err)
				return err
			}
			captures[i] = capture
			node.Done(newCaptureSample(capture.SizeBytes, capture.StoredBytes, max(capture.SizeBytes-capture.StoredBytes, 0)))
			log.Debug().Dur("duration", time.Since(started)).Int64("size_bytes", capture.SizeBytes).
				Msg("writable layer capture finished")
			return nil
		})
	}
	err := eg.Wait()
	batch.End(err)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Str("snapshot_id", snapshotID.String()).
			Msg("the partial writable layer capture is retained for a later gc")
		return nil, err
	}
	return captures, nil
}

// captureImages stores every distinct image concurrently, apart from the writable layers.
// inResult marks the warning of an image not stored as InResult.
func (s *Service) captureImages(
	ctx context.Context, snapshotID model.SavepointID, reqs []model.ImageCaptureRequest, inResult bool,
) ([]model.Image, error) {
	if len(reqs) == 0 {
		return nil, nil
	}

	images := make([]model.Image, len(reqs))
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindImageCapture}, len(reqs))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, req := range reqs {
		eg.Go(func() error {
			opCtx := zerolog.Ctx(egCtx).With().
				Str("image_id", req.Image.ID.String()).
				Strs("image", req.Image.Refs).
				Str("snapshot_id", snapshotID.String()).
				Str("op", "save").
				Logger().
				WithContext(egCtx)
			opCtx, node := progress.Run(opCtx, newImageStep(req.Image), progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("image capture started")
			image, err := s.imageBackend.CaptureImage(opCtx, req)
			if err != nil {
				err = fmt.Errorf("capture image %s: %w", req.Image.ID, err)
				node.End(err)
				return err
			}
			images[i] = image
			if !image.Stored {
				warn(node, "not stored: a replica left out of the capture cannot be rebuilt from it", inResult)
			}
			node.Done(newCaptureSample(image.SizeBytes, image.StoredBytes, max(image.SizeBytes-image.StoredBytes, 0)))
			log.Debug().Dur("duration", time.Since(started)).Bool("stored", image.Stored).
				Int64("stored_bytes", image.StoredBytes).Msg("image capture finished")
			return nil
		})
	}
	err := eg.Wait()
	batch.End(err)
	if err != nil {
		return nil, err
	}
	return images, nil
}

// restoreWritableLayers loads every writable layer the savepoint holds back into the daemon,
// concurrently, and names each under a tag of its own. A savepoint holding no
// writable layer restores nothing.
func (s *Service) restoreWritableLayers(
	ctx context.Context, savepoint model.Savepoint, composeProject string,
) ([]model.WritableLayerRestoreResult, error) {
	if len(savepoint.WritableLayers) == 0 {
		zerolog.Ctx(ctx).Info().Str("snapshot_id", savepoint.ID.String()).
			Msg("the savepoint holds no writable layers; nothing to restore")
		return nil, nil
	}

	restored := make([]model.WritableLayerRestoreResult, len(savepoint.WritableLayers))
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindLayerRestore}, len(savepoint.WritableLayers))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, capture := range savepoint.WritableLayers {
		eg.Go(func() error {
			opCtx := imageRestoreContext(egCtx, capture, savepoint.ID)
			opCtx, node := progress.Run(opCtx, progress.Step{Kind: kindLayer, ID: capture.ContainerName},
				progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("writable layer restore started")
			image, err := s.imageBackend.RestoreWritableLayer(opCtx, model.WritableLayerRestoreRequest{
				SnapshotID: savepoint.ID,
				Layer:      capture,
				Tag:        restoredLayerTag(composeProject, savepoint.ID, capture),
			})
			if err != nil {
				err = fmt.Errorf("restore writable layer of %s/%d: %w", capture.Service, capture.Replica, err)
				node.End(err)
				return err
			}
			restored[i] = image
			node.Done(newRestoreSample(capture.SizeBytes))
			log.Debug().Dur("duration", time.Since(started)).Str("tag", image.Tag).
				Msg("writable layer restore finished")
			return nil
		})
	}
	err := eg.Wait()
	batch.End(err)
	if err != nil {
		return nil, err
	}
	return restored, nil
}

// requireImages refuses, before anything is stopped, a service the rollback would build
// on an image compose may not pull and the daemon does not hold.
func (s *Service) requireImages(ctx context.Context, project *types.Project, restored []model.WritableLayerRestoreResult) error {
	for _, name := range slices.Sorted(maps.Keys(project.Services)) {
		svc := project.Services[name]
		if svc.PullPolicy != types.PullPolicyNever ||
			slices.ContainsFunc(restored, func(r model.WritableLayerRestoreResult) bool { return r.Service == name }) {
			continue
		}
		present, err := s.imageLookup.ImageRefExists(ctx, svc.Image)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("%w: service %s runs on %s", errImageUnavailable, name, svc.Image)
		}
	}
	return nil
}

// retargetImages points every restored service, on a copy of the project, at its lowest
// recorded replica's image with pull_policy never: the tag exists only in the local daemon.
func retargetImages(project *types.Project, restored []model.WritableLayerRestoreResult) *types.Project {
	if len(restored) == 0 {
		return project
	}

	lowest := map[string]model.WritableLayerRestoreResult{}
	for _, layer := range restored {
		if current, ok := lowest[layer.Service]; !ok || layer.Replica < current.Replica {
			lowest[layer.Service] = layer
		}
	}
	retargeted := *project
	retargeted.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		if layer, ok := lowest[name]; ok {
			service.Image = layer.Tag
			service.PullPolicy = types.PullPolicyNever
		}
		retargeted.Services[name] = service
	}
	return &retargeted
}

// rebuildDivergentReplicas rebuilds each replica the compose spec cannot name on its own layer
// and recorded number. It must run before the volume restore, which resolves through containers.
func (s *Service) rebuildDivergentReplicas(ctx context.Context, replicas []recordedReplica) error {
	if createdAsRecorded(replicas) {
		return nil
	}

	introspection, err := s.composeInspector.Introspect(ctx)
	if err != nil {
		return fmt.Errorf("introspecting the rebuilt compose project: %w", err)
	}
	rebuilds, err := newReplicaRebuilds(replicas, builtContainers(introspection))
	if err != nil {
		return err
	}

	ctx, batch := progress.Batch(ctx, progress.Step{Kind: kindReplicaRebuild}, len(rebuilds))
	for _, rebuild := range rebuilds {
		if err := s.rebuildReplica(ctx, rebuild); err != nil {
			batch.End(err)
			return err
		}
	}
	batch.End(nil)
	return nil
}

// rebuildReplica recreates one created container as the replica the savepoint recorded.
func (s *Service) rebuildReplica(ctx context.Context, rebuild replicaRebuild) error {
	log := zerolog.Ctx(ctx)
	replica := rebuild.recorded
	ctx, node := progress.Run(ctx, progress.Step{Kind: kindReplica, ID: replica.containerName})
	if replica.number != rebuild.created {
		log.Warn().Str("service", replica.service).Int("replica", replica.number).Int("created_replica", rebuild.created).
			Msg("compose numbers replicas without gaps and created this one under another number; " +
				"it is recreated under the number and name the savepoint recorded")
		node.Warn(fmt.Sprintf("created as replica %d, recreated as %d", rebuild.created, replica.number))
	}
	identity := model.ReplicaIdentity{Name: replica.containerName, Number: replica.number}
	rebuilt, err := s.containerRebuilder.RecreateWithImage(ctx, rebuild.containerID, identity, replica.image)
	node.End(err)
	if err != nil {
		return fmt.Errorf("rebuild replica %d of service %s from %s: %w",
			replica.number, replica.service, replica.image, err)
	}
	log.Info().Str("service", replica.service).Int("replica", replica.number).
		Str("container", rebuilt).Str("image", replica.image).Msg("replica rebuilt as the savepoint recorded it")
	return nil
}

// restoredLayerTag names one restored layer in the local daemon:
// arcave/<project>-<service>-<replica>-<base image>:sp-<savepoint id>. The
// repository carries the identity and the tag the savepoint, so `docker images`
// reads as one row per savepoint under a row per replica.
func restoredLayerTag(composeProject string, snapshotID model.SavepointID, capture model.WritableLayer) string {
	return restoredImageRepository(composeProject, capture) + ":" + model.RestoredLayerTagPrefix + snapshotID.String()
}

// restoredImageRepository builds the repository half and keeps it addressable: the
// base image is the one segment carrying no identity, so it is what gets cut when
// the name would pass docker's cap, and the replica is still named without it.
func restoredImageRepository(composeProject string, capture model.WritableLayer) string {
	identity := model.RestoredLayerTagNamespace + "/" +
		joinSegments(refSegment(composeProject), refSegment(capture.Service), strconv.Itoa(capture.Replica))

	base := refSegment(capture.UpstreamImageRef)
	room := maxRepositoryPath - len(identity) - 1
	if base == "" || room <= 0 {
		return identity
	}
	if len(base) > room {
		base = strings.TrimRight(base[:room], "-")
	}
	if base == "" {
		return identity
	}
	return identity + "-" + base
}

// joinSegments joins on the one separator refSegment leaves behind and drops the
// segments that reduced to nothing: a path component may not open on a separator.
func joinSegments(segments ...string) string {
	kept := segments[:0]
	for _, segment := range segments {
		if segment != "" {
			kept = append(kept, segment)
		}
	}
	return strings.Join(kept, "-")
}

// refSegment reduces one segment to lower-case alphanumerics joined by single
// dashes — a subset of every docker path component, so whatever a service or an
// image is called, what comes out of here is nameable.
func refSegment(segment string) string {
	var builder strings.Builder
	dashed := true // no leading separator: a path component opens alphanumeric
	for _, char := range strings.ToLower(segment) {
		switch {
		case (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9'):
			builder.WriteRune(char)
			dashed = false
		case !dashed:
			builder.WriteByte('-')
			dashed = true
		}
	}
	return strings.TrimRight(builder.String(), "-")
}

// newRecordedReplicas lists every replica savepoint holds under the number and name it
// recorded: one with a restored layer on its tag, one without on its service's own
// image. A restore resolves an anonymous volume by recorded number, so a gap in the
// numbering is rebuilt without a layer too.
func newRecordedReplicas(
	savepoint model.Savepoint, project *types.Project, restored []model.WritableLayerRestoreResult,
) []recordedReplica {
	replicas := make([]recordedReplica, 0, len(restored))
	for _, layer := range restored {
		replicas = append(replicas, recordedReplica{
			service:       layer.Service,
			number:        layer.Replica,
			containerName: layer.ContainerName,
			image:         layer.Tag,
		})
	}
	if savepoint.NotCovered == nil {
		return replicas
	}
	for _, skipped := range savepoint.NotCovered.WritableLayers {
		svc, ok := project.Services[skipped.Service]
		if !ok {
			continue
		}
		replicas = append(replicas, recordedReplica{
			service:       skipped.Service,
			number:        skipped.Replica,
			containerName: skipped.Container,
			image:         svc.Image,
		})
	}
	return replicas
}

// replicasByService groups the replicas by service, each group ascending by replica,
// and lists the services in the order they first appear.
func replicasByService(replicas []recordedReplica) ([]string, map[string][]recordedReplica) {
	var services []string
	byService := map[string][]recordedReplica{}
	for _, replica := range replicas {
		if _, ok := byService[replica.service]; !ok {
			services = append(services, replica.service)
		}
		byService[replica.service] = append(byService[replica.service], replica)
	}
	for _, group := range byService {
		slices.SortFunc(group, func(a, b recordedReplica) int { return cmp.Compare(a.number, b.number) })
	}
	return services, byService
}

// createdAsRecorded reports whether every service's replicas count from 1 without a gap,
// all on the one image the service was retargeted to. It trusts compose to number a
// freshly created service 1..N, which is what lets it skip reading the created containers.
func createdAsRecorded(replicas []recordedReplica) bool {
	_, byService := replicasByService(replicas)
	for _, group := range byService {
		for i, replica := range group {
			if replica.number != i+1 || replica.image != group[0].image {
				return false
			}
		}
	}
	return true
}

// newReplicaRebuilds is the replicas whose image or number compose did not reproduce, highest
// recorded number first per service so each claimed name has already been freed.
func newReplicaRebuilds(replicas []recordedReplica, built map[string]map[int]string) ([]replicaRebuild, error) {
	services, byService := replicasByService(replicas)
	var rebuilds []replicaRebuild
	for _, service := range services {
		group := byService[service]
		created := slices.Sorted(maps.Keys(built[service]))
		pairs, err := newReplicaPairs(replicaNumbers(group), created)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", service, err)
		}
		for i := len(group) - 1; i >= 0; i-- {
			replica, pair := group[i], pairs[i]
			if replica.image == group[0].image && pair.recorded == pair.created {
				continue
			}
			rebuilds = append(rebuilds, replicaRebuild{
				recorded: replica, created: pair.created, containerID: built[service][pair.created],
			})
		}
	}
	return rebuilds, nil
}

// newReplicaPairs pairs recorded with created replica numbers, both ascending: [1,3] against
// [1,2] is 1←1, 3←2. Fewer created than recorded is an error.
func newReplicaPairs(recorded, created []int) ([]replicaPair, error) {
	recorded, created = slices.Sorted(slices.Values(recorded)), slices.Sorted(slices.Values(created))
	if len(created) < len(recorded) {
		return nil, fmt.Errorf("the rebuilt compose project holds %d replicas to restore %d recorded ones into",
			len(created), len(recorded))
	}
	pairs := make([]replicaPair, len(recorded))
	for i, number := range recorded {
		pairs[i] = replicaPair{recorded: number, created: created[i]}
	}
	return pairs, nil
}

// replicaNumbers is the number of each replica, in order.
func replicaNumbers(replicas []recordedReplica) []int {
	numbers := make([]int, len(replicas))
	for i, replica := range replicas {
		numbers[i] = replica.number
	}
	return numbers
}

// builtContainers indexes the compose project's containers by service and replica number.
func builtContainers(introspection model.IntrospectionResult) map[string]map[int]string {
	built := map[string]map[int]string{}
	for _, svc := range introspection.Project.Services {
		for _, container := range svc.Containers {
			if built[svc.Name] == nil {
				built[svc.Name] = map[int]string{}
			}
			built[svc.Name][container.Number] = container.ID
		}
	}
	return built
}

// newImageStep names an image's node by its first reference, else its id.
func newImageStep(image model.Image) progress.Step {
	var name string
	if len(image.Refs) > 0 {
		name = image.Refs[0]
	}
	return progress.Step{
		Kind: kindImage,
		ID:   image.ID.String(),
		Name: name,
	}
}

// imageOpContext attributes every line the capture logs.
func imageOpContext(
	ctx context.Context, container model.ContainerRef, snapshotID model.SavepointID,
) context.Context {
	return zerolog.Ctx(ctx).With().
		Str("service", container.Service).
		Int("replica", container.Replica).
		Str("container", container.ID).
		Str("snapshot_id", snapshotID.String()).
		Str("op", "commit").
		Logger().
		WithContext(ctx)
}

// imageRestoreContext attributes every line the restore logs.
func imageRestoreContext(
	ctx context.Context, capture model.WritableLayer, snapshotID model.SavepointID,
) context.Context {
	return zerolog.Ctx(ctx).With().
		Str("service", capture.Service).
		Int("replica", capture.Replica).
		Str("image_id", capture.CommittedImageID.String()).
		Str("snapshot_id", snapshotID.String()).
		Str("op", "load").
		Logger().
		WithContext(ctx)
}
