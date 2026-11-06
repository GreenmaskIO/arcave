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
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// endedRecorder renders every step's last event as "<kind>[<id>] <status> [bytes]".
type endedRecorder struct {
	steps map[progress.StepID]progress.Step
	ended []string
}

func (r *endedRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		r.steps[evt.StepID] = evt.Open.Step
	}
	if !evt.Status.IsTerminal() {
		return
	}
	step := r.steps[evt.StepID]
	line := fmt.Sprintf("%s[%s] %s", step.Kind, step.ID, evt.Status)
	if evt.Sample.Bytes != nil {
		line += fmt.Sprintf(" bytes=%d", evt.Sample.Bytes.Done)
	}
	if evt.Sample.Items != nil {
		line += fmt.Sprintf(" items=%d/%d", evt.Sample.Items.Done, evt.Sample.Items.Failed)
	}
	r.ended = append(r.ended, line)
}

func TestDeleteOpsProgress(t *testing.T) {
	tests := []struct {
		name   string
		ops    []model.GarbageOp
		delErr map[string]error
		want   []string
	}{
		{
			name: "each op ends with what it freed, the batch with its count",
			ops: []model.GarbageOp{
				{Target: "a", Bytes: utils.New(int64(10))},
				{Target: "c"},
			},
			want: []string{
				"garbage[a] succeeded bytes=10",
				"garbage[c] succeeded",
				"gc.delete[] succeeded items=2/0",
			},
		},
		{
			name: "a failed op fails the batch",
			ops: []model.GarbageOp{
				{Target: "a", Bytes: utils.New(int64(10))},
				{Target: "b", Bytes: utils.New(int64(20))},
			},
			delErr: map[string]error{"b": errors.New("denied")},
			want: []string{
				"garbage[a] succeeded bytes=10",
				"garbage[b] failed",
				"gc.delete[] failed items=1/1",
			},
		},
		{
			name:   "a target already gone succeeds, freeing nothing",
			ops:    []model.GarbageOp{{Target: "a", Bytes: utils.New(int64(10))}},
			delErr: map[string]error{"a": storages.ErrFileNotFound},
			want: []string{
				"garbage[a] succeeded",
				"gc.delete[] succeeded items=1/0",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			recorder := &endedRecorder{steps: map[progress.StepID]progress.Step{}}
			tracker := progress.New(recorder, fixedNow)
			ctx, root := tracker.Begin(zerolog.Nop().WithContext(context.Background()), progress.Step{Kind: kindGC})
			del := &mockDel{}
			for _, op := range tt.ops {
				del.On("del", mock.Anything, op.Target).Return(tt.delErr[op.Target]).Once()
			}

			// Act
			DeleteOps(ctx, tt.ops, del.del)
			root.End(nil)

			// Assert
			assert.Equal(t, append(tt.want, "gc[] succeeded"), recorder.ended)
			del.AssertExpectations(t)
		})
	}
}

func TestEndCollectorNode(t *testing.T) {
	tests := []struct {
		name   string
		report model.GarbageReport
		want   []string
	}{
		{
			name:   "a clean report says what it found",
			report: model.GarbageReport{Summary: model.GarbageSummary{Found: 3}},
			want:   []string{`in_progress msg="3 found"`, `succeeded msg="3 found"`},
		},
		{
			name: "what it held back or could not judge is a warning",
			report: model.GarbageReport{Summary: model.GarbageSummary{
				Found:    1,
				Skipped:  2,
				Unjudged: 4,
				Aborted:  "no daemon",
			}},
			want: []string{
				`warned msg="aborted: no daemon" in_result`,
				`warned msg="4 object(s) not judged" in_result`,
				`warned msg="2 object(s) held back" in_result`,
				`in_progress msg="1 found"`,
				`succeeded msg="1 found"`,
			},
		},
		{
			name:   "a collector's failure fails its step with its error",
			report: model.GarbageReport{Error: "store unreachable"},
			want:   []string{`failed error="store unreachable"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			recorder := &eventRecorder{}
			tracker := progress.New(recorder, fixedNow)
			ctx, _ := tracker.Begin(zerolog.Nop().WithContext(context.Background()), progress.Step{Kind: kindGC})
			_, step := progress.Run(ctx, progress.Step{Kind: kindCollector})

			// Act
			endCollectorStep(ctx, step, tt.report)

			// Assert
			assert.Equal(t, tt.want, recorder.lines)
		})
	}
}

// eventRecorder renders every event of the collector's step, the root's opening left out.
type eventRecorder struct {
	lines []string
}

func (r *eventRecorder) Render(evt progress.Event) {
	if evt.StepID != 2 || evt.Status == progress.StatusStarted {
		return
	}
	line := string(evt.Status)
	if evt.Msg != "" {
		line += fmt.Sprintf(" msg=%q", evt.Msg)
	}
	if evt.Error != "" {
		line += fmt.Sprintf(" error=%q", evt.Error)
	}
	if evt.InResult {
		line += " in_result"
	}
	r.lines = append(r.lines, line)
}

func fixedNow() time.Time {
	return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
}

func TestNewDeletionError(t *testing.T) {
	errLost := errors.New("lock lost")
	tests := []struct {
		name     string
		deletion *model.GarbageDeletion
		stopped  bool
		want     string
		wantIs   error
	}{
		{
			name: "no deletion is no error",
		},
		{
			name:     "every op run is no error",
			deletion: &model.GarbageDeletion{Deleted: []model.GarbageDeleted{{Target: "a"}}},
		},
		{
			name:     "a failure is counted",
			deletion: &model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{{Target: "a"}}},
			want:     "1 deletion(s) failed, 0 not run",
		},
		{
			name:     "a deletion the stop cut short ends with the stop's cause",
			deletion: &model.GarbageDeletion{NotRun: 2},
			stopped:  true,
			want:     errLost.Error(),
			wantIs:   errLost,
		},
		{
			name:     "a failure the stop came with ends with the stop's cause",
			deletion: &model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{{Target: "a", Interrupted: true}}},
			stopped:  true,
			want:     errLost.Error(),
			wantIs:   errLost,
		},
		{
			name:     "a failure past the stop is counted",
			deletion: &model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{{Target: "a"}}},
			stopped:  true,
			want:     "1 deletion(s) failed, 0 not run",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, stop := context.WithCancelCause(context.Background())
			defer stop(nil)
			if tt.stopped {
				stop(errLost)
			}

			// Act
			err := newDeletionError(ctx, tt.deletion)

			// Assert
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.want)
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}
