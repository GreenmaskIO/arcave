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
	"fmt"
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// The kinds of the steps a capture opens. compose.start, volume and layer are duplicated in core;
// keep both in step. compose.stop is capture's alone.
const (
	kindSavepointCapture progress.Kind = "savepoint.capture"

	kindComposeStop  progress.Kind = "compose.stop"
	kindComposeStart progress.Kind = "compose.start"

	kindVolumeCapture progress.Kind = "volume.capture"
	kindVolume        progress.Kind = "volume"
	kindImageCapture  progress.Kind = "image.capture"
	kindImage         progress.Kind = "image"
	kindLayerCapture  progress.Kind = "layer.capture"
	kindLayer         progress.Kind = "layer"
)

// newNotCoveredWarning sums up what a capture leaves out, "" for nothing.
func newNotCoveredWarning(notCovered *model.NotCovered) string {
	if notCovered == nil {
		return ""
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(len(notCovered.Mounts), "mount(s)")
	add(len(notCovered.ServicesWithoutVolumes), "service(s) without volumes")
	add(len(notCovered.KilledContainers), "killed container(s)")
	add(len(notCovered.ReplicaGaps), "replica gap(s)")
	add(len(notCovered.UnresolvedImages), "unresolved image(s)")
	add(len(notCovered.WritableLayers), "writable layer(s)")
	add(len(notCovered.Images), "image(s)")
	if len(parts) == 0 {
		return ""
	}
	return "not covered: " + strings.Join(parts, ", ")
}

// warn reports msg on step, marked InResult when inResult is set.
func warn(step *progress.StepReporter, msg string, inResult bool) {
	if inResult {
		step.WarnInResult(msg)
		return
	}
	step.Warn(msg)
}

// isInResult reports whether a capture under labels is its run's result, i.e. no safety snapshot.
func isInResult(labels map[string]string) bool {
	return !model.Savepoint{Labels: labels}.IsSafetySnapshot()
}

// newImageSample is what capturing image moved: nothing for one not stored.
func newImageSample(image model.Image) progress.Sample {
	if !image.Stored {
		return progress.Sample{}
	}
	return newCaptureSample(image.SizeBytes, image.StoredBytes, max(image.SizeBytes-image.StoredBytes, 0))
}

// newCaptureSample is the sample a captured unit ends with: its size, what it stored, and what of
// it was found already stored.
func newCaptureSample(size, stored, reused int64) progress.Sample {
	return progress.Sample{Bytes: &progress.Bytes{
		Done:   size,
		Total:  size,
		Stored: stored,
		Reused: reused,
	}}
}
