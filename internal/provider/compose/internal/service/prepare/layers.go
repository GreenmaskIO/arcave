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

package prepare

import (
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

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps a layer restore opens.
const (
	kindLayerRestore progress.Kind = "layer.restore"
	kindLayer        progress.Kind = "layer"
)

// errImageUnavailable refuses a rollback that would rebuild a service on an image
// neither the savepoint nor the daemon holds, and compose may not pull.
var errImageUnavailable = errors.New("image is neither in the savepoint nor in the daemon")

// maxRepositoryPath is docker's own cap on a repository path, the registry domain
// aside — RepositoryNameTotalLengthMax of github.com/distribution/reference.
const maxRepositoryPath = 255

// restoreWritableLayers loads every writable layer the savepoint holds back into the daemon,
// concurrently, each under its own tag.
func (s *Preparer) restoreWritableLayers(
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
			opCtx, step := progress.Run(opCtx, progress.Step{Kind: kindLayer, ID: capture.ContainerName},
				progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("writable layer restore started")
			image, err := s.layerRestorer.RestoreWritableLayer(opCtx, model.WritableLayerRestoreRequest{
				SnapshotID: savepoint.ID,
				Layer:      capture,
				Tag:        restoredLayerTag(composeProject, savepoint.ID, capture),
			})
			if err != nil {
				err = fmt.Errorf("restore writable layer of %s/%d: %w", capture.Service, capture.Replica, err)
				step.End(err)
				return err
			}
			restored[i] = image
			step.Done(composemodel.NewRestoreSample(capture.SizeBytes))
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
func (s *Preparer) requireImages(ctx context.Context, project *types.Project, restored []model.WritableLayerRestoreResult) error {
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
