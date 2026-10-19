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
)

// capturePolicyChange is one resource a capture takes otherwise than the latest savepoint
// did, as the two specs' stamps say.
type capturePolicyChange struct {
	service  string
	stampKey string
	previous string
	current  string
}

// warnCapturePolicyDrift warns of every resource the latest savepoint captured otherwise
// than this capture will: a label added or dropped, an override file left out, the labels
// a rollback put back. Best-effort: a spec it cannot read is only logged.
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
	for _, change := range findCapturePolicyDrift(previous, current) {
		log.Warn().Str("service", change.service).Str("stamp", change.stampKey).
			Str("was", change.previous).Str("now", change.current).
			Msg("captured otherwise than in the latest savepoint")
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
			if !strings.HasPrefix(key, model.CaptureStampPrefix) {
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
