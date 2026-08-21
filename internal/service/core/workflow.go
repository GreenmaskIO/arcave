package core

import (
	"context"
	"fmt"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// workingSet is what a mutating workflow operates on: the introspection taken once
// before the stop, the request's bound, and one backend per live volume.
type workingSet struct {
	project     *types.Project
	waitTimeout time.Duration
	backends    []model.VolumeSnapshotBackend
}

// prepare introspects the stack and builds one backend per live volume. A capture
// covering nothing is refused here — a rollback with no undo is not a rollback.
func (s *Service) prepare(ctx context.Context, waitTimeout time.Duration) (workingSet, error) {
	res, err := s.composeInspector.Introspect(ctx)
	if err != nil {
		return workingSet{}, fmt.Errorf("introspecting the stack: %w", err)
	}

	// Before the stop, so a spec that cannot be rebuilt leaves the project running.
	project, err := s.projectAssembler.Assemble(res)
	if err != nil {
		return workingSet{}, fmt.Errorf("assembling the compose spec: %w", err)
	}

	volumes, err := s.resolveBackupVolumes(ctx, res)
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

	return workingSet{project: project, waitTimeout: waitTimeout, backends: backends}, nil
}

// stopServices puts the services down.
func (s *Service) stopServices(ctx context.Context, ws workingSet) error {
	zerolog.Ctx(ctx).Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, ws.project); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}
	return nil
}

// startServices brings the services back and waits on their health checks.
func (s *Service) startServices(ctx context.Context, ws workingSet) error {
	zerolog.Ctx(ctx).Info().Msg("starting services")
	if err := s.composeRunner.Start(ctx, ws.project, ws.waitTimeout); err != nil {
		return fmt.Errorf("starting services: %w", err)
	}
	return nil
}
