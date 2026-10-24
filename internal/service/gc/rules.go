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

package gc

import (
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

// NewFindings starts a stat's findings with an empty op list, so a kind that found nothing
// reports [] rather than null.
func NewFindings() model.GarbageFindings {
	return model.GarbageFindings{Ops: []model.GarbageOp{}}
}

// HoldForCaptures holds target back while a capture of the scope is in progress or of unknown
// status, and reports whether it did.
func HoldForCaptures(findings *model.GarbageFindings, scope ComposeProjectScope, target string, lastModified time.Time) bool {
	switch {
	case len(scope.InProgress) > 0:
		findings.Skipped = append(findings.Skipped, model.GarbageSkip{
			Target:       target,
			Reason:       model.GarbageReasonCaptureInProgress,
			LastModified: lastModified,
		})
	case len(scope.Unknown) > 0:
		findings.Unjudged = append(findings.Unjudged, model.GarbageSkip{
			Target:       target,
			Reason:       model.GarbageReasonStatusUnknown,
			LastModified: lastModified,
		})
	default:
		return false
	}
	return true
}

// CapturesHeld reports whether HoldForCaptures holds every shared object of scope.
func CapturesHeld(scope ComposeProjectScope) bool {
	return len(scope.InProgress) > 0 || len(scope.Unknown) > 0
}

// NewLiveImages maps every image the savepoints hold the bytes of to the first savepoint naming it.
func NewLiveImages(savepoints []model.Savepoint) map[model.ImageDigestID]model.SavepointID {
	live := map[model.ImageDigestID]model.SavepointID{}
	for _, savepoint := range savepoints {
		for _, image := range savepoint.Images {
			if _, seen := live[image.ID]; image.Stored && !seen {
				live[image.ID] = savepoint.ID
			}
		}
	}
	return live
}
