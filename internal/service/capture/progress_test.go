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
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestNewNotCoveredWarning(t *testing.T) {
	tests := []struct {
		name       string
		notCovered *model.NotCovered
		want       string
	}{
		{
			name: "nothing left out",
		},
		{
			name:       "an empty report",
			notCovered: &model.NotCovered{},
		},
		{
			name: "every kind left out is counted",
			notCovered: &model.NotCovered{
				Mounts:           []model.SkippedVolume{{}, {}},
				KilledContainers: []model.KilledContainer{{}},
			},
			want: "not covered: 2 mount(s), 1 killed container(s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newNotCoveredWarning(tt.notCovered)

			assert.Equal(t, tt.want, got)
		})
	}
}

// A capture's warnings are marked InResult, a safety snapshot's are not.
func TestWarn(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   []bool
	}{
		{
			name: "an ordinary savepoint is the result",
			want: []bool{true},
		},
		{
			name:   "a safety snapshot is not",
			labels: map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
			want:   []bool{false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				recorder = &warnedRecorder{}
				now      = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
				ctx      = zerolog.Nop().WithContext(context.Background())
			)
			_, step := progress.New(recorder, now).Begin(ctx, progress.Step{Kind: kindSavepointCapture})

			warn(step, "not covered: 1 mount(s)", isInResult(tt.labels))

			assert.Equal(t, tt.want, recorder.inResult)
		})
	}
}

func TestNewImageSample(t *testing.T) {
	tests := []struct {
		name  string
		image model.Image
		want  progress.Sample
	}{
		{
			name: "stored by this capture",
			image: model.Image{
				SizeBytes:   100,
				StoredBytes: 40,
				Stored:      true,
			},
			want: newCaptureSample(100, 40, 60),
		},
		{
			name: "already stored",
			image: model.Image{
				SizeBytes: 100,
				Stored:    true,
			},
			want: newCaptureSample(100, 0, 100),
		},
		{
			name: "not stored moves nothing",
			image: model.Image{
				SizeBytes: 100,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newImageSample(tt.image)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
