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

package workload

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
)

// liveWorkload is the compose project as one introspection found it.
type liveWorkload struct {
	service       *composeWorkload
	introspection model.IntrospectionResult
}

// Inspect reads the compose project as the daemon holds it now, and refuses a daemon the image
// backend cannot serve.
func (s *composeWorkload) Inspect(ctx context.Context) (provider.LiveWorkload, error) {
	introspection, err := s.inspector.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	return liveWorkload{
		service:       s,
		introspection: introspection,
	}, nil
}

// Absent reports whether the daemon holds no container of the compose project.
func (l liveWorkload) Absent() bool {
	return composemodel.IsAbsent(l.introspection)
}

// PlanCapture decides the capture of the live compose project as opts says. The record of
// opts.Restoring is read first, and one that cannot be read is no provider.CapturePolicyError.
func (l liveWorkload) PlanCapture(ctx context.Context, opts provider.CaptureOptions) (provider.CapturePlan, error) {
	var (
		restoring *model.Savepoint
		plan      composemodel.Plan
		err       error
	)
	if opts.Restoring != "" {
		savepoint, err := sessionstore.LoadSavepoint(ctx, l.service.savepointRepo, opts.Restoring)
		if err != nil {
			return nil, err
		}
		restoring = &savepoint
	}
	if plan, err = l.service.planner.Plan(ctx, l.introspection, restoring); err != nil {
		return nil, err
	}
	return l.service.newCapturePlan(plan, l.introspection), nil
}
