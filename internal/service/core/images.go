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
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// maxRepositoryPath is docker's own cap on a repository path, the registry domain
// aside — RepositoryNameTotalLengthMax of github.com/distribution/reference.
const maxRepositoryPath = 255

// replicaPair is one recorded replica and the replica compose created in its place.
type replicaPair struct {
	recorded int
	created  int
}

// replicaRebuild is one restored layer and the created container it is rebuilt over.
type replicaRebuild struct {
	image       model.ImageRestore
	created     int
	containerID string
}

// captureImages commits every container's writable layer under one savepoint id,
// concurrently, and returns one capture per container in input order.
func (s *Service) captureImages(
	ctx context.Context, snapshotID model.SavepointID, containers []model.ContainerRef,
) ([]model.ImageCapture, error) {
	if len(containers) == 0 {
		return nil, nil
	}
	if err := snapshotID.Validate(); err != nil {
		return nil, fmt.Errorf("validate savepoint id: %w", err)
	}

	captures := make([]model.ImageCapture, len(containers))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, container := range containers {
		eg.Go(func() error {
			opCtx := imageOpContext(egCtx, container, snapshotID)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Info().Msg("image capture started")
			capture, err := s.imageBackend.Capture(opCtx, model.ImageCaptureRequest{
				SnapshotID: snapshotID,
				Container:  container,
			})
			if err != nil {
				return fmt.Errorf("capture image of %s/%d: %w", container.Service, container.Replica, err)
			}
			captures[i] = capture
			log.Info().Dur("duration", time.Since(started)).Int64("size_bytes", capture.SizeBytes).
				Msg("image capture finished")
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		zerolog.Ctx(ctx).Warn().Str("snapshot_id", snapshotID.String()).
			Msg("the partial image capture is retained for a later gc")
		return nil, err
	}
	return captures, nil
}

// captureUpstreams keeps every distinct upstream image, concurrently, apart from
// the layers: a savepoint holds its upstreams whether or not it holds a layer.
func (s *Service) captureUpstreams(
	ctx context.Context, snapshotID model.SavepointID, reqs []model.UpstreamImageCaptureRequest,
) ([]model.UpstreamImage, error) {
	if len(reqs) == 0 {
		return nil, nil
	}

	images := make([]model.UpstreamImage, len(reqs))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, req := range reqs {
		eg.Go(func() error {
			opCtx := zerolog.Ctx(egCtx).With().
				Str("upstream_image_id", req.Image.ID.String()).
				Str("snapshot_id", snapshotID.String()).
				Str("op", "save").
				Logger().
				WithContext(egCtx)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Info().Msg("upstream image capture started")
			image, err := s.imageBackend.CaptureUpstream(opCtx, req)
			if err != nil {
				return fmt.Errorf("capture upstream image %s: %w", req.Image.ID, err)
			}
			images[i] = image
			log.Info().Dur("duration", time.Since(started)).Bool("stored", image.Stored).
				Int64("stored_bytes", image.StoredBytes).Msg("upstream image capture finished")
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return images, nil
}

// restoreImages loads every writable layer the record holds back into the daemon,
// concurrently, and names each under a tag of its own. A record predating image
// capture restores nothing.
func (s *Service) restoreImages(
	ctx context.Context, record model.Savepoint, project string,
) ([]model.ImageRestore, error) {
	if len(record.Images) == 0 {
		zerolog.Ctx(ctx).Info().Str("snapshot_id", record.ID.String()).
			Msg("the savepoint holds no writable layers; nothing to restore")
		return nil, nil
	}

	restored := make([]model.ImageRestore, len(record.Images))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.maxConcurrentOperations)
	for i, capture := range record.Images {
		eg.Go(func() error {
			opCtx := imageRestoreContext(egCtx, capture, record.ID)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Info().Msg("image restore started")
			image, err := s.imageBackend.Restore(opCtx, model.ImageRestoreRequest{
				SnapshotID: record.ID,
				Capture:    capture,
				ImageRef:   restoredImageRef(project, record.ID, capture),
			})
			if err != nil {
				return fmt.Errorf("restore image of %s/%d: %w", capture.Service, capture.Replica, err)
			}
			restored[i] = image
			log.Info().Dur("duration", time.Since(started)).Str("image_ref", image.ImageRef).
				Msg("image restore finished")
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return restored, nil
}

// retargetImages points every restored service, on a copy of the project, at its lowest
// recorded replica's image with pull_policy never: the tag exists only in the local daemon.
func retargetImages(project *types.Project, restored []model.ImageRestore) *types.Project {
	if len(restored) == 0 {
		return project
	}

	_, byService := restoredByService(restored)
	retargeted := *project
	retargeted.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		if images, ok := byService[name]; ok {
			service.Image = images[0].ImageRef
			service.PullPolicy = types.PullPolicyNever
		}
		retargeted.Services[name] = service
	}
	return &retargeted
}

// rebuildDivergentReplicas rebuilds each replica the compose spec cannot name on its own layer
// and recorded number. It must run before the volume restore, which resolves through containers.
func (s *Service) rebuildDivergentReplicas(ctx context.Context, restored []model.ImageRestore) error {
	if createdAsRecorded(restored) {
		return nil
	}

	res, err := s.composeInspector.Introspect(ctx)
	if err != nil {
		return fmt.Errorf("introspecting the rebuilt compose project: %w", err)
	}
	rebuilds, err := replicaRebuilds(restored, builtContainers(res))
	if err != nil {
		return err
	}

	log := zerolog.Ctx(ctx)
	for _, r := range rebuilds {
		image := r.image
		if image.Replica != r.created {
			log.Warn().Str("service", image.Service).Int("replica", image.Replica).Int("created_replica", r.created).
				Msg("compose numbers replicas without gaps and created this one under another number; " +
					"it is recreated under the number and name the savepoint recorded")
		}
		as := model.ReplicaIdentity{Name: image.ContainerName, Number: image.Replica}
		rebuilt, err := s.containerRebuilder.RecreateWithImage(ctx, r.containerID, as, image.ImageRef)
		if err != nil {
			return fmt.Errorf("rebuild replica %d of service %s from %s: %w",
				image.Replica, image.Service, image.ImageRef, err)
		}
		log.Info().Str("service", image.Service).Int("replica", image.Replica).
			Str("container", rebuilt).Str("image_ref", image.ImageRef).Msg("replica rebuilt on its own layer")
	}
	return nil
}

// restoredImageRef names one restored layer in the local daemon:
// arcave/<project>-<service>-<replica>-<base image>:sp-<savepoint id>. The
// repository carries the identity and the tag the savepoint, so `docker images`
// reads as one row per savepoint under a row per replica.
func restoredImageRef(project string, snapshotID model.SavepointID, capture model.ImageCapture) string {
	return restoredImageRepository(project, capture) + ":" + model.LayerTagPrefix + snapshotID.String()
}

// restoredImageRepository builds the repository half and keeps it addressable: the
// base image is the one segment carrying no identity, so it is what gets cut when
// the name would pass docker's cap, and the replica is still named without it.
func restoredImageRepository(project string, capture model.ImageCapture) string {
	identity := model.LayerImageNamespace + "/" +
		joinSegments(refSegment(project), refSegment(capture.Service), strconv.Itoa(capture.Replica))

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
func refSegment(s string) string {
	var b strings.Builder
	dashed := true // no leading separator: a path component opens alphanumeric
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dashed = false
		case !dashed:
			b.WriteByte('-')
			dashed = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// restoredByService groups the restored layers by service, each group ascending
// by replica, and lists the services in the order they first appear.
func restoredByService(restored []model.ImageRestore) ([]string, map[string][]model.ImageRestore) {
	var services []string
	byService := map[string][]model.ImageRestore{}
	for _, image := range restored {
		if _, ok := byService[image.Service]; !ok {
			services = append(services, image.Service)
		}
		byService[image.Service] = append(byService[image.Service], image)
	}
	for _, images := range byService {
		slices.SortFunc(images, func(a, b model.ImageRestore) int { return cmp.Compare(a.Replica, b.Replica) })
	}
	return services, byService
}

// createdAsRecorded reports whether every service's replicas count from 1 without a gap,
// all on the one image the service was retargeted to. It trusts compose to number a
// freshly created service 1..N, which is what lets it skip reading the created containers.
func createdAsRecorded(restored []model.ImageRestore) bool {
	_, byService := restoredByService(restored)
	for _, images := range byService {
		for i, image := range images {
			if image.Replica != i+1 || image.ImageRef != images[0].ImageRef {
				return false
			}
		}
	}
	return true
}

// replicaRebuilds is the replicas whose layer or number compose did not reproduce, highest
// recorded number first per service so each claimed name has already been freed.
func replicaRebuilds(
	restored []model.ImageRestore, built map[string]map[int]string,
) ([]replicaRebuild, error) {
	services, byService := restoredByService(restored)
	var rebuilds []replicaRebuild
	for _, service := range services {
		images := byService[service]
		created := slices.Sorted(maps.Keys(built[service]))
		pairs, err := pairReplicas(replicaNumbers(images), created)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", service, err)
		}
		for i := len(images) - 1; i >= 0; i-- {
			image, pair := images[i], pairs[i]
			if image.ImageRef == images[0].ImageRef && pair.recorded == pair.created {
				continue
			}
			rebuilds = append(rebuilds, replicaRebuild{
				image: image, created: pair.created, containerID: built[service][pair.created],
			})
		}
	}
	return rebuilds, nil
}

// pairReplicas pairs recorded with created replica numbers, both ascending: [1,3] against
// [1,2] is 1←1, 3←2. Fewer created than recorded is an error.
func pairReplicas(recorded, created []int) ([]replicaPair, error) {
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

// replicaNumbers is the replica number of each layer, in order.
func replicaNumbers(images []model.ImageRestore) []int {
	numbers := make([]int, len(images))
	for i, image := range images {
		numbers[i] = image.Replica
	}
	return numbers
}

// builtContainers indexes the compose project's containers by service and replica number.
func builtContainers(res model.IntrospectionResult) map[string]map[int]string {
	built := map[string]map[int]string{}
	for _, svc := range res.Project.Services {
		for _, c := range svc.Containers {
			if built[svc.Name] == nil {
				built[svc.Name] = map[int]string{}
			}
			built[svc.Name][c.Number] = c.ID
		}
	}
	return built
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
	ctx context.Context, capture model.ImageCapture, snapshotID model.SavepointID,
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
