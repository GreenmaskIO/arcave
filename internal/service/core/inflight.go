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
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// putInflightMarker writes the marker for the rollback restoring targetID; safety is the safety
// snapshot it names, empty for none. A phase change rewrites it whole.
func (s *Service) putInflightMarker(ctx context.Context, phase model.InflightPhase, targetID, safety model.SavepointID) error {
	return s.inflightMarker.Put(ctx, model.InflightMarker{
		Phase:          phase,
		Session:        s.session,
		ComposeProject: s.composeProject,
		Target:         targetID,
		Safety:         safety,
		At:             time.Now(),
	})
}
