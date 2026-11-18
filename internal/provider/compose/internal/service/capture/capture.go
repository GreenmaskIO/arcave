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
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// storedSavepoint is what a capture stored under its id, short of the record.
type storedSavepoint struct {
	stats          []model.SnapshotStat
	images         []model.Image
	writableLayers []model.WritableLayer
}

// capturedSavepoint is a capture stored and not committed: its record, written by Commit.
type capturedSavepoint struct {
	capturer  *Capturer
	savepoint model.Savepoint
}

// Commit writes the record, the last object; core names the savepoint in a failure.
func (c capturedSavepoint) Commit(ctx context.Context) (model.Savepoint, error) {
	if err := c.capturer.savepointRepo.SaveSavepoint(ctx, c.savepoint); err != nil {
		return model.Savepoint{}, err
	}
	return c.savepoint, nil
}

// Capture stores a savepoint of a project already held still under req.ID, short of its record;
// killed are the containers the stop killed. On error the handle is nil and what was stored is
// left to a gc.
func (s *Capturer) Capture(
	ctx context.Context, plan composemodel.Plan, killed []string, req provider.CaptureRequest,
) (_ provider.CapturedSavepoint, err error) {
	var (
		createdAt  = time.Now()
		notCovered = plan.NewNotCovered(killed)
		stored     storedSavepoint
	)
	ctx, step := progress.Begin(ctx, progress.Step{
		Kind: kindSavepointCapture,
		Name: cmp.Or(req.Name, req.ID.String()),
	})
	defer func() { step.End(err) }()
	if warning := newNotCoveredWarning(notCovered); warning != "" {
		warn(step, warning, req.InResult)
	}
	if err = req.ID.Validate(); err != nil {
		return nil, err
	}
	if stored, err = s.captureSavepoint(ctx, req.ID, req, plan); err != nil {
		return nil, err
	}
	return capturedSavepoint{
		capturer: s,
		savepoint: model.Savepoint{
			ID:                    req.ID,
			Name:                  req.Name,
			Description:           req.Description,
			Labels:                req.Labels,
			CreatedAt:             createdAt,
			TakenBeforeRollbackTo: req.TakenBefore,
			Stats:                 stored.stats,
			Images:                stored.images,
			WritableLayers:        stored.writableLayers,
			Spec:                  &model.SpecRef{Format: model.SpecFormatComposeYAML},
			NotCovered:            notCovered,
		},
	}, nil
}

// captureSavepoint stores everything a savepoint holds but its record: volumes, images, layers,
// and the spec written beside them.
func (s *Capturer) captureSavepoint(
	ctx context.Context, id model.SavepointID, req provider.CaptureRequest, plan composemodel.Plan,
) (storedSavepoint, error) {
	var (
		stats          []model.SnapshotStat
		images         []model.Image
		writableLayers []model.WritableLayer
		err            error
	)
	if stats, err = s.captureVolumes(ctx, id, req.Labels, plan.Volumes); err != nil {
		return storedSavepoint{}, err
	}
	// The images first: a writable layer's stored_bytes is then what it adds over its image.
	if images, err = s.captureImages(ctx, id, plan.Images, req.InResult); err != nil {
		return storedSavepoint{}, err
	}
	if writableLayers, err = s.captureWritableLayers(ctx, id, plan.WritableLayers); err != nil {
		return storedSavepoint{}, err
	}
	if err = s.savepointRepo.SaveSpec(ctx, id, plan.Spec); err != nil {
		return storedSavepoint{}, fmt.Errorf("write compose spec of savepoint %s: %w", id, err)
	}
	return storedSavepoint{
		stats:          stats,
		images:         images,
		writableLayers: writableLayers,
	}, nil
}
