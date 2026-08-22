package core

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

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
			capture, err := s.imageCapturer.Capture(opCtx, model.ImageCaptureRequest{
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
