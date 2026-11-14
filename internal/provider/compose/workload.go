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

package compose

import (
	"context"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// liveWorkload is the compose project as one introspection found it.
type liveWorkload struct {
	service       *Service
	introspection model.IntrospectionResult
}

// Inspect reads the compose project as the daemon holds it now, and refuses a daemon the image
// backend cannot serve.
func (s *Service) Inspect(ctx context.Context) (provider.LiveWorkload, error) {
	introspection, err := s.introspect(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireDaemon(ctx, s.daemonVersion, s.minDaemonMajor); err != nil {
		return nil, err
	}
	return liveWorkload{
		service:       s,
		introspection: introspection,
	}, nil
}

// Absent reports whether the daemon holds no container of the compose project.
func (l liveWorkload) Absent() bool {
	return composeProjectAbsent(l.introspection)
}

// PlanCapture decides the capture of the live compose project as opts says.
func (l liveWorkload) PlanCapture(ctx context.Context, opts provider.CaptureOptions) (provider.CapturePlan, error) {
	plan, err := l.service.buildPlan(ctx, l.introspection, opts)
	if err != nil {
		return nil, err
	}
	return l.service.newCapturePlan(plan, l.introspection), nil
}

// introspect reads the compose project as the daemon holds it now.
func (s *Service) introspect(ctx context.Context) (model.IntrospectionResult, error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindProjectIntrospect})
	introspection, err := s.composeInspector.Introspect(ctx)
	step.End(err)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("introspect compose project: %w", err)
	}
	return introspection, nil
}

// composeProjectAbsent reports whether the daemon holds no container of the compose project:
// it was never started, or was taken down.
func composeProjectAbsent(introspection model.IntrospectionResult) bool {
	return len(introspection.Project.Services) == 0
}

// ownedVolumes are the docker names of the volumes the compose project owns.
func ownedVolumes(introspection model.IntrospectionResult) map[string]bool {
	owned := map[string]bool{}
	for _, v := range introspection.Project.Volumes {
		if !v.External {
			owned[v.Name] = true
		}
	}
	return owned
}

// newAnonymousVolumeNames indexes the docker names of the compose project's anonymous volumes by
// the identity their data is captured under.
func newAnonymousVolumeNames(introspection model.IntrospectionResult) map[string]string {
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
