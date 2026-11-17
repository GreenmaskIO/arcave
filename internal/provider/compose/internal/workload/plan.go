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
	"time"

	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/hold"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// capturePlan is one capture decided, nothing touched yet: the anonymous volumes a teardown of the
// project it holds orphans, and the volumes the project owned when it was inspected.
type capturePlan struct {
	service              *composeWorkload
	plan                 composemodel.Plan
	anonymousVolumeNames map[string]string
	ownedBefore          map[string]bool
}

// newCapturePlan wraps plan of the introspected project in the handle core holds a capture by.
func (s *composeWorkload) newCapturePlan(plan composemodel.Plan, introspection composemodel.IntrospectionResult) capturePlan {
	return capturePlan{
		service:              s,
		plan:                 plan,
		anonymousVolumeNames: composemodel.NewAnonymousVolumeNames(introspection),
		ownedBefore:          composemodel.OwnedVolumes(introspection),
	}
}

// LabelsIgnored is why the plan follows the config, nil when it reads the labels.
func (p capturePlan) LabelsIgnored() error {
	return p.plan.LabelsIgnored
}

// Hold holds the project still as the plan says; the killed containers ride on what it returns.
func (p capturePlan) Hold(ctx context.Context) (provider.HeldWorkload, error) {
	killed, err := p.service.hold(ctx, p.plan)
	if err != nil {
		return nil, err
	}
	return heldWorkload{
		service:              p.service,
		plan:                 p.plan,
		killed:               killed,
		anonymousVolumeNames: p.anonymousVolumeNames,
		ownedBefore:          p.ownedBefore,
	}, nil
}

// Release brings back the project the plan holds; it converges whether Hold
// ran, failed partway or never ran.
func (p capturePlan) Release(ctx context.Context, waitTimeout time.Duration) error {
	return p.service.release(ctx, p.plan, waitTimeout)
}

// hold holds the project still as plan says and returns the containers the stop killed.
func (s *composeWorkload) hold(ctx context.Context, plan composemodel.Plan) (_ []string, err error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindSavepointHold}, progress.MeasureResources)
	defer func() { step.End(err) }()

	return hold.NewTriggersByMode(s.composeRunner, s.containerControl, plan, 0).BeforeCapture(ctx)
}

// release brings back the project plan holds, waiting waitTimeout on its health checks.
func (s *composeWorkload) release(revertCtx context.Context, plan composemodel.Plan, waitTimeout time.Duration) (err error) {
	revertCtx, step := progress.Run(revertCtx, progress.Step{Kind: kindSavepointRelease}, progress.MeasureResources)
	defer func() { step.End(err) }()

	return hold.NewTriggersByMode(s.composeRunner, s.containerControl, plan, waitTimeout).AfterCapture(revertCtx)
}
