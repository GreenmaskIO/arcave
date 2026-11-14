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
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/captureplan"
	"github.com/greenmaskio/arcave/internal/provider/compose/hold"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// capturePlan is one capture decided, nothing touched yet: the anonymous volumes a teardown of the
// project it holds orphans, and the volumes the project owned when it was inspected.
type capturePlan struct {
	service              *Provider
	plan                 captureplan.Plan
	anonymousVolumeNames map[string]string
	ownedBefore          map[string]bool
}

// newCapturePlan wraps plan of the introspected project in the handle core holds a capture by.
func (s *Provider) newCapturePlan(plan captureplan.Plan, introspection model.IntrospectionResult) capturePlan {
	return capturePlan{
		service:              s,
		plan:                 plan,
		anonymousVolumeNames: newAnonymousVolumeNames(introspection),
		ownedBefore:          ownedVolumes(introspection),
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

// heldWorkload is the project heldWorkload still, the containers its stop killed, the anonymous volumes its
// teardown orphans and the volumes it owned when it was inspected.
type heldWorkload struct {
	service              *Provider
	plan                 captureplan.Plan
	killed               []string
	anonymousVolumeNames map[string]string
	ownedBefore          map[string]bool
}

// Capture stores what is held under a new savepoint, not yet committed.
func (h heldWorkload) Capture(ctx context.Context, req provider.CaptureRequest) (provider.CapturedSavepoint, error) {
	return h.service.capturer.Capture(ctx, h.plan, h.killed, req)
}

// PrepareSavepoint loads the layers of the savepoint id names back into the daemon and points the
// held project at them, over the backends that captured it: it reads no spec and refuses no image.
func (h heldWorkload) PrepareSavepoint(
	ctx context.Context, id model.SavepointID, opts provider.PrepareOptions,
) (provider.PreparedSavepoint, error) {
	savepoint, err := h.service.loadSavepoint(ctx, id)
	if err != nil {
		return nil, err
	}
	restored, err := h.service.restoreWritableLayers(ctx, savepoint, h.plan.Project.Name)
	if err != nil {
		return nil, err
	}
	return preparedSavepoint{
		savepoint:  savepoint,
		project:    retargetImages(h.plan.Project, restored),
		restored:   restored,
		backends:   h.plan.Volumes,
		deployment: opts.Deployment,
	}, nil
}

// Teardown runs `compose down` of the held project.
func (h heldWorkload) Teardown(ctx context.Context) (provider.VacantWorkload, error) {
	if err := h.service.composeDown(ctx, h.plan.Project); err != nil {
		return nil, err
	}
	return vacantWorkload{
		service:              h.service,
		anonymousVolumeNames: h.anonymousVolumeNames,
		ownedBefore:          h.ownedBefore,
		downProject:          h.plan.Project,
	}, nil
}

// hold holds the project still as plan says and returns the containers the stop killed.
func (s *Provider) hold(ctx context.Context, plan captureplan.Plan) (_ []string, err error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindSavepointHold}, progress.MeasureResources)
	defer func() { step.End(err) }()

	return hold.NewTriggersByMode(s.composeRunner, s.containerControl, plan, 0).BeforeCapture(ctx)
}

// release brings back the project plan holds, waiting waitTimeout on its health checks.
func (s *Provider) release(revertCtx context.Context, plan captureplan.Plan, waitTimeout time.Duration) (err error) {
	revertCtx, step := progress.Run(revertCtx, progress.Step{Kind: kindSavepointRelease}, progress.MeasureResources)
	defer func() { step.End(err) }()

	return hold.NewTriggersByMode(s.composeRunner, s.containerControl, plan, waitTimeout).AfterCapture(revertCtx)
}
