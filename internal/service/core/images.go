package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// restoredImageNamespace opens every tag a rollback puts on a restored layer, so
// what arcave left in the daemon is one prefix away from being told apart.
const restoredImageNamespace = "arcave"

// restoredTagPrefix opens the tag half: what the repository does not say is which
// savepoint the layer came from.
const restoredTagPrefix = "sp-"

// maxRepositoryPath is docker's own cap on a repository path, the registry domain
// aside — RepositoryNameTotalLengthMax of github.com/distribution/reference.
const maxRepositoryPath = 255

// firstReplica is the replica number compose counts from, and the only one a
// compose service's single image can stand for.
const firstReplica = 1

// replicaKey addresses one replica of one service among the containers a rebuild
// has just created.
type replicaKey struct {
	service string
	replica int
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

// retargetImages points every restored service at its first replica's image, on a
// copy of the project. The tag lives only in the local daemon, so the rebuild is
// told never to pull it.
func retargetImages(project *types.Project, restored []model.ImageRestore) *types.Project {
	if len(restored) == 0 {
		return project
	}

	refs := make(map[string]string, len(restored))
	for _, image := range restored {
		if image.Replica == firstReplica {
			refs[image.Service] = image.ImageRef
		}
	}

	retargeted := *project
	retargeted.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		if ref, ok := refs[name]; ok {
			service.Image = ref
			service.PullPolicy = types.PullPolicyNever
		}
		retargeted.Services[name] = service
	}
	return &retargeted
}

// rebuildDivergentReplicas gives every replica past the first its own layer, over
// what compose has just created. It runs before the volumes are restored: an
// anonymous volume is resolved through the container that mounts it, so the
// container set has to be final by then.
func (s *Service) rebuildDivergentReplicas(ctx context.Context, restored []model.ImageRestore) error {
	divergent := divergentReplicas(restored)
	if len(divergent) == 0 {
		return nil
	}

	res, err := s.composeInspector.Introspect(ctx)
	if err != nil {
		return fmt.Errorf("introspecting the rebuilt stack: %w", err)
	}
	built := builtContainers(res)

	for _, image := range divergent {
		key := replicaKey{service: image.Service, replica: image.Replica}
		containerID, ok := built[key]
		if !ok {
			return fmt.Errorf("the rebuilt stack holds no replica %d of service %s to restore its layer into",
				image.Replica, image.Service)
		}
		rebuilt, err := s.containerRebuilder.RecreateWithImage(ctx, containerID, image.ImageRef)
		if err != nil {
			return fmt.Errorf("rebuild replica %d of service %s from %s: %w",
				image.Replica, image.Service, image.ImageRef, err)
		}
		zerolog.Ctx(ctx).Info().Str("service", image.Service).Int("replica", image.Replica).
			Str("container", rebuilt).Str("image_ref", image.ImageRef).Msg("replica rebuilt on its own layer")
	}
	return nil
}

// restoredImageRef names one restored layer in the local daemon:
// arcave/<project>-<service>-<replica>-<base image>:sp-<savepoint id>. The
// repository carries the identity and the tag the savepoint, so `docker images`
// reads as one row per savepoint under a row per replica.
func restoredImageRef(project string, snapshotID model.SavepointID, capture model.ImageCapture) string {
	return restoredImageRepository(project, capture) + ":" + restoredTagPrefix + snapshotID.String()
}

// restoredImageRepository builds the repository half and keeps it addressable: the
// base image is the one segment carrying no identity, so it is what gets cut when
// the name would pass docker's cap, and the replica is still named without it.
func restoredImageRepository(project string, capture model.ImageCapture) string {
	identity := restoredImageNamespace + "/" +
		joinSegments(refSegment(project), refSegment(capture.Service), strconv.Itoa(capture.Replica))

	base := refSegment(unnestBaseImage(identity, capture.BaseImage))
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

// unnestBaseImage recovers the image a replica really came from when its base is a
// tag an earlier rollback left behind. Without it every rollback would fold its own
// name into the next one's, and the name would grow without bound.
func unnestBaseImage(identity, base string) string {
	if repository, tag, ok := strings.Cut(base, ":"); ok && strings.HasPrefix(tag, restoredTagPrefix) {
		base = repository
	}
	return strings.TrimPrefix(base, identity+"-")
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

// divergentReplicas is the replicas the compose spec cannot name: those past the
// first whose image differs from the one their service was retargeted to.
func divergentReplicas(restored []model.ImageRestore) []model.ImageRestore {
	first := make(map[string]string, len(restored))
	for _, image := range restored {
		if image.Replica == firstReplica {
			first[image.Service] = image.ImageRef
		}
	}

	var divergent []model.ImageRestore
	for _, image := range restored {
		if image.Replica != firstReplica && first[image.Service] != image.ImageRef {
			divergent = append(divergent, image)
		}
	}
	return divergent
}

// builtContainers indexes the stack's containers by the replica each one is.
func builtContainers(res model.IntrospectionResult) map[replicaKey]string {
	built := map[replicaKey]string{}
	for _, svc := range res.Project.Services {
		for _, c := range svc.Containers {
			built[replicaKey{service: svc.Name, replica: c.Number}] = c.ID
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
		Str("image_id", capture.ImageID).
		Str("snapshot_id", snapshotID.String()).
		Str("op", "load").
		Logger().
		WithContext(ctx)
}
