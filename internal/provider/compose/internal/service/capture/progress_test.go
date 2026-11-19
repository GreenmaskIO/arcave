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

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestNewNotCoveredWarning(t *testing.T) {
	tests := []struct {
		name       string
		notCovered *composemodel.NotCovered
		want       string
	}{
		{
			name: "nothing left out",
		},
		{
			name:       "an empty report",
			notCovered: &composemodel.NotCovered{},
		},
		{
			name: "every kind left out is counted",
			notCovered: &composemodel.NotCovered{
				Mounts:           []composemodel.SkippedVolume{{}, {}},
				KilledContainers: []composemodel.KilledContainer{{}},
				FailedHooks:      []composemodel.FailedHook{{}},
			},
			want: "not covered: 2 mount(s), 1 killed container(s), 1 failed hook(s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newNotCoveredWarning(tt.notCovered)

			assert.Equal(t, tt.want, got)
		})
	}
}

// A warning is marked InResult when the capture is the command's result.
func TestWarn(t *testing.T) {
	tests := []struct {
		name     string
		inResult bool
		want     []bool
	}{
		{
			name:     "the command's result",
			inResult: true,
			want:     []bool{true},
		},
		{
			name: "not the command's result",
			want: []bool{false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var (
				recorder = &composetest.WarnedRecorder{}
				now      = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
				ctx      = zerolog.Nop().WithContext(context.Background())
			)
			_, step := progress.New(recorder, now).Begin(ctx, progress.Step{Kind: kindSavepointCapture})

			// Act
			warn(step, "not covered: 1 mount(s)", tt.inResult)

			// Assert
			assert.Equal(t, tt.want, recorder.InResult)
		})
	}
}

func TestNewImageSample(t *testing.T) {
	tests := []struct {
		name  string
		image composemodel.Image
		want  progress.Sample
	}{
		{
			name: "stored by this capture",
			image: composemodel.Image{
				SizeBytes:   100,
				StoredBytes: 40,
				Stored:      true,
			},
			want: newCaptureSample(100, 40, 60),
		},
		{
			name: "already stored",
			image: composemodel.Image{
				SizeBytes: 100,
				Stored:    true,
			},
			want: newCaptureSample(100, 0, 100),
		},
		{
			name: "not stored moves nothing",
			image: composemodel.Image{
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
