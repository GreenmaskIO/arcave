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
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// capturePolicyChange is one resource a capture takes otherwise than the latest savepoint
// did, as the two specs' stamps say.
type capturePolicyChange struct {
	service  string
	stampKey string
	previous string
	current  string
}

// warnCapturePolicyDrift warns, once per service, of every unit the latest savepoint captured
// otherwise than this capture will; a spec it cannot read is only logged.
func (s *Service) warnCapturePolicyDrift(ctx context.Context, savepoints []model.Savepoint, current *types.Project) {
	latest, ok := findLatestSavepoint(savepoints)
	if !ok {
		return
	}
	log := zerolog.Ctx(ctx).With().Str("savepoint_id", latest.ID.String()).Str("savepoint", latest.Name).Logger()
	previous, err := s.loadSavepointSpec(ctx, latest)
	if err != nil {
		log.Warn().Err(err).Msg("cannot compare the capture policy with the latest savepoint")
		return
	}
	var (
		changes = findCapturePolicyDrift(previous, current)
		node    = progress.Current(ctx)
		ref     = cmp.Or(latest.Name, latest.ID.String())
	)
	for _, change := range changes {
		log.Info().Str("service", change.service).Str("stamp", change.stampKey).
			Str("was", change.previous).Str("now", change.current).
			Msg("captured otherwise than in the latest savepoint")
	}
	for _, serviceChanges := range newServiceChanges(changes) {
		node.Warn(newDriftWarning(serviceChanges, ref))
	}
}

// newServiceChanges groups changes, sorted by service, one group per service.
func newServiceChanges(changes []capturePolicyChange) [][]capturePolicyChange {
	var groups [][]capturePolicyChange
	for i, change := range changes {
		if i == 0 || change.service != changes[i-1].service {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], change)
	}
	return groups
}

// newDriftWarning renders one service's changes as one line, each unit's stamp before and now.
func newDriftWarning(changes []capturePolicyChange, ref string) string {
	units := make([]string, 0, len(changes))
	for _, change := range changes {
		unit := strings.Replace(strings.TrimPrefix(change.stampKey, model.CaptureStampLabelPrefix), ".", " ", 1)
		units = append(units, fmt.Sprintf("%s %s → %s",
			unit, formatCaptureStamp(change.previous), formatCaptureStamp(change.current)))
	}
	return fmt.Sprintf("%s captured otherwise than in savepoint %s: %s",
		changes[0].service, ref, strings.Join(units, ", "))
}

// formatCaptureStamp renders a stamp as "left out", else its backend.
func formatCaptureStamp(stamp string) string {
	parsed, err := model.ParseCaptureStamp(stamp)
	switch {
	case err != nil:
		return stamp
	case !parsed.Capture:
		return "left out"
	default:
		return cmp.Or(parsed.Backend, "kept")
	}
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
// differ. A resource only one of them has is a change of topology, not of policy, and
// is left out.
func findCapturePolicyDrift(previousProject, currentProject *types.Project) []capturePolicyChange {
	var changes []capturePolicyChange
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
			if !ok || !isCaptureChanged(previousStamp, currentLabels[key]) {
				continue
			}
			changes = append(changes, capturePolicyChange{
				service: serviceName, stampKey: key, previous: previousStamp, current: currentLabels[key],
			})
		}
	}
	return changes
}

// isCaptureChanged reports whether two stamps capture differently; a stamp it cannot read
// claims nothing.
func isCaptureChanged(previous, current string) bool {
	previousStamp, err := model.ParseCaptureStamp(previous)
	if err != nil {
		return false
	}
	currentStamp, err := model.ParseCaptureStamp(current)
	if err != nil {
		return false
	}
	return previousStamp.Capture != currentStamp.Capture || previousStamp.Backend != currentStamp.Backend
}
