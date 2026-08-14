package core

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// workingSet is what a mutating workflow operates on and with: the two knobs its
// request carried and one backend per live volume. Assembled under the lock and
// before anything is stopped, so a failure here leaves the project running.
type workingSet struct {
	composeFiles []string
	waitTimeout  time.Duration
	backends     []dockermodel.VolumeSnapshotBackend
}

// prepare resolves the live volumes and builds their backends. A capture covering
// nothing is refused here — a rollback with no undo is not a rollback.
func (s *Service) prepare(ctx context.Context, composeFiles []string, waitTimeout time.Duration) (workingSet, error) {
	volumes, err := s.resolveBackupVolumes(ctx, composeFiles)
	if err != nil {
		return workingSet{}, err
	}
	if len(volumes) == 0 {
		return workingSet{}, errNoVolumesToBackUp
	}

	backends, err := s.buildBackends(volumes)
	if err != nil {
		return workingSet{}, err
	}

	return workingSet{composeFiles: composeFiles, waitTimeout: waitTimeout, backends: backends}, nil
}

// stopServices puts the services down.
func (s *Service) stopServices(ctx context.Context, ws workingSet) error {
	zerolog.Ctx(ctx).Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, ws.composeFiles...); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}
	return nil
}

// startServices brings the services back and waits on their health checks.
func (s *Service) startServices(ctx context.Context, ws workingSet) error {
	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Up(ctx, ws.waitTimeout, ws.composeFiles...); err != nil {
		return fmt.Errorf("starting services: %w", err)
	}
	return nil
}
