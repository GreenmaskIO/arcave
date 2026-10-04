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
	"fmt"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// workingSet is what a mutating workflow operates on: the introspection taken once
// before the stop, its rendered spec, the request's bound, and one backend per volume.
type workingSet struct {
	project     *types.Project
	spec        []byte
	waitTimeout time.Duration
	backends    []model.VolumeSnapshotBackend
	// containers are the replicas whose writable layer a capture commits.
	containers []model.ContainerRef
	// upstreams are the distinct images the containers' compose file named.
	upstreams []model.UpstreamImageCaptureRequest
	// anonVolumes maps a mount's ObjectID to the docker name behind it — the only
	// record of what a teardown orphans.
	anonVolumes map[string]string
}

// prepare introspects the stack, renders its spec and builds one backend per live
// volume. A capture covering nothing is refused here.
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
	spec, err := s.projectAssembler.Marshal(project)
	if err != nil {
		return workingSet{}, fmt.Errorf("rendering the compose spec: %w", err)
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

	containers := capturableContainers(ctx, res)

	return workingSet{
		project:     project,
		spec:        spec,
		waitTimeout: waitTimeout,
		backends:    backends,
		containers:  containers,
		upstreams:   upstreamImages(res, containers),
		anonVolumes: anonymousVolumes(res),
	}, nil
}

// stopServices puts the services down.
func (s *Service) stopServices(ctx context.Context, ws workingSet) error {
	zerolog.Ctx(ctx).Info().Msg("stopping services")
	if err := s.composeRunner.Stop(ctx, ws.project); err != nil {
		return fmt.Errorf("stopping services: %w", err)
	}
	return nil
}

// tearDownStack removes the stack's containers and networks. It goes by project
// name, so it also takes down what a failed rebuild left standing.
func (s *Service) tearDownStack(ctx context.Context, project *types.Project) error {
	zerolog.Ctx(ctx).Info().Msg("removing the stack")
	if err := s.composeRunner.Down(ctx, project); err != nil {
		return fmt.Errorf("removing the stack: %w", err)
	}
	return nil
}

// buildStack creates the containers of project without starting them: the data is
// restored into them first.
func (s *Service) buildStack(ctx context.Context, project *types.Project) error {
	zerolog.Ctx(ctx).Info().Msg("rebuilding the stack from the savepoint's spec")
	if err := s.composeRunner.Create(ctx, project); err != nil {
		return fmt.Errorf("rebuilding the stack: %w", err)
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

// anonymousVolumes indexes the docker names of the stack's anonymous volumes by
// the identity their data is captured under.
func anonymousVolumes(res model.IntrospectionResult) map[string]string {
	names := map[string]string{}
	for _, svc := range res.Project.Services {
		for _, v := range svc.Volumes {
			if v.Type == model.VolumeTypeAnon && v.VolumeName != "" {
				names[v.ObjectID(svc.Name)] = v.VolumeName
			}
		}
	}
	return names
}
