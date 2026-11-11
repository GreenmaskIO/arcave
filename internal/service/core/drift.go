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
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// warnCapturePolicyDrift warns, once per service, of every unit the latest savepoint captured
// otherwise than this capture will, and returns that drift for the record; nil when nothing changed
// or the latest savepoint's spec cannot be read, which is only logged.
func (s *Service) warnCapturePolicyDrift(
	ctx context.Context, savepoints []model.Savepoint, current *types.Project,
) *model.CaptureDrift {
	latest, ok := findLatestSavepoint(savepoints)
	if !ok {
		return nil
	}
	log := zerolog.Ctx(ctx).With().Str("savepoint_id", latest.ID.String()).Str("savepoint", latest.Name).Logger()
	previous, err := s.loadSavepointSpec(ctx, latest)
	if err != nil {
		log.Warn().Err(err).Msg("cannot compare the capture policy with the latest savepoint")
		return nil
	}
	changes := findCapturePolicyDrift(previous, current)
	if len(changes) == 0 {
		return nil
	}
	for _, change := range changes {
		log.Info().Str("service", change.Service).Str("stamp", change.Stamp).
			Str("was", change.Was.String()).Str("now", change.Now.String()).
			Msg("captured otherwise than in the latest savepoint")
	}
	drift := &model.CaptureDrift{
		Since:   model.SavepointRef{ID: latest.ID, Name: latest.Name},
		Changes: changes,
	}
	step := progress.Current(ctx)
	for _, notice := range drift.Notices() {
		step.WarnInResult(notice.Message)
	}
	return drift
}

// findLatestSavepoint is the newest of savepoints: a SavepointID is a UUIDv7, so the greatest.
func findLatestSavepoint(savepoints []model.Savepoint) (model.Savepoint, bool) {
	if len(savepoints) == 0 {
		return model.Savepoint{}, false
	}
	return slices.MaxFunc(savepoints, func(a, b model.Savepoint) int {
		return strings.Compare(string(a.ID), string(b.ID))
	}), true
}

// findCapturePolicyDrift lists the stamps both specs carry whose capture or backend
// differ, sorted by service and stamp. A resource only one of them has is a change of
// topology, not of policy, and is left out; so is a stamp either spec cannot read.
func findCapturePolicyDrift(previousProject, currentProject *types.Project) []model.CaptureChange {
	var changes []model.CaptureChange
	for _, serviceName := range slices.Sorted(maps.Keys(currentProject.Services)) {
		previousService, ok := previousProject.Services[serviceName]
		if !ok {
			continue
		}
		currentLabels := currentProject.Services[serviceName].Labels
		for _, key := range slices.Sorted(maps.Keys(currentLabels)) {
			if !strings.HasPrefix(key, model.CaptureStampLabelPrefix) {
				continue
			}
			previousStamp, ok := previousService.Labels[key]
			if !ok {
				continue
			}
			was, now, ok := parseCaptureChange(previousStamp, currentLabels[key])
			if !ok {
				continue
			}
			changes = append(changes, model.CaptureChange{
				Service: serviceName,
				Stamp:   strings.TrimPrefix(key, model.CaptureStampLabelPrefix),
				Was:     was,
				Now:     now,
			})
		}
	}
	return changes
}

// parseCaptureChange reads two stamps' decisions, ok when they capture differently; a stamp
// it cannot read claims nothing.
func parseCaptureChange(previous, current string) (was, now model.CaptureStampDecision, ok bool) {
	previousStamp, err := model.ParseCaptureStamp(previous)
	if err != nil {
		return was, now, false
	}
	currentStamp, err := model.ParseCaptureStamp(current)
	if err != nil {
		return was, now, false
	}
	was, now = model.NewCaptureStampDecision(previousStamp), model.NewCaptureStampDecision(currentStamp)
	return was, now, was != now
}
