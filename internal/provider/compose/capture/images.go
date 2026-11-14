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

package capture

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// captureImages stores every distinct image concurrently, apart from the writable layers.
// inResult marks the warning of an image not stored as InResult.
func (s *Capturer) captureImages(
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
			opCtx, step := progress.Run(opCtx, newImageStep(req.Image), progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("image capture started")
			image, err := s.imageBackend.CaptureImage(opCtx, req)
			if err != nil {
				err = fmt.Errorf("capture image %s: %w", req.Image.ID, err)
				step.End(err)
				return err
			}
			images[i] = image
			if !image.Stored {
				warn(step, "not stored: a replica left out of the capture cannot be rebuilt from it", inResult)
			}
			step.Done(newImageSample(image))
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

// newImageStep names an image's step by its first reference, else its id.
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

// captureWritableLayers commits every requested writable layer under one savepoint id,
// concurrently, and returns one capture per request in input order.
func (s *Capturer) captureWritableLayers(
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
			opCtx, step := progress.Run(opCtx, progress.Step{
				Kind: kindLayer,
				ID:   container.Name,
			}, progress.MeasureBytes)
			log := zerolog.Ctx(opCtx)

			started := time.Now()
			log.Debug().Msg("writable layer capture started")
			capture, err := s.imageBackend.CaptureWritableLayer(opCtx, req)
			if err != nil {
				err = fmt.Errorf("capture writable layer of %s/%d: %w", container.Service, container.Replica, err)
				step.End(err)
				return err
			}
			captures[i] = capture
			step.Done(newCaptureSample(capture.SizeBytes, capture.StoredBytes, max(capture.SizeBytes-capture.StoredBytes, 0)))
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
