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

package compose

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// Compose's progress is what turns a two-minute health-check wait from a hang
// into something a user can watch, so every channel must reach the library — and
// none of them may reach stdout, which carries arcave's JSON.
func TestNewComposeProgress(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		wantProcessor api.EventProcessor // nil means "the event logger"
		wantStream    io.Writer
	}{
		{
			name:       "log folds compose's events into arcave's own log",
			mode:       config.ProgressLog,
			wantStream: io.Discard,
		},
		{
			// Plain off a terminal, as here: compose's tree would spray ANSI escapes into a log file.
			name:          "tty hands stderr back to compose's own renderer",
			mode:          config.ProgressTTY,
			wantProcessor: display.Plain(io.Discard),
			wantStream:    os.Stderr,
		},
		{
			name:          "quiet discards the stream as well as the UI",
			mode:          config.ProgressQuiet,
			wantProcessor: display.Quiet(),
			wantStream:    io.Discard,
		},
		{
			name:       "an unset channel is the log",
			mode:       "",
			wantStream: io.Discard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newComposeProgress(tt.mode, zerolog.Nop())

			// Assert
			assert.Equal(t, tt.wantStream, got.stream)
			if tt.wantProcessor == nil {
				require.NotNil(t, got.events)
				assert.Same(t, got.events, got.processor, "the logger compose reports to is the one a step reads")
				return
			}
			assert.IsType(t, tt.wantProcessor, got.processor)
			assert.Nil(t, got.events)
		})
	}
}

// eventRecorder keeps every event it is handed.
type eventRecorder struct {
	events []progress.Event
}

func (r *eventRecorder) Render(evt progress.Event) {
	r.events = append(r.events, evt)
}

func TestComposeEventSink_Event(t *testing.T) {
	// Arrange
	recorder := &eventRecorder{}
	ctx := progress.WithTracker(zerolog.Nop().WithContext(context.Background()), progress.New(recorder, time.Now))
	ctx, _ = progress.Begin(ctx, progress.Step{Kind: "savepoint.create"})
	ctx, step := progress.Run(ctx, progress.Step{Kind: "savepoint.hold"}, progress.MeasureResources)
	sink := newComposeEventSink(ctx)
	require.NotNil(t, sink)

	// Act
	sink.Event(composeclient.Event{Kind: "Container", Name: "demo-db-1", Status: "Stopping", State: composeclient.EventWorking})
	sink.Event(composeclient.Event{Kind: "Container", Name: "demo-db-1", Status: "Stopped", State: composeclient.EventDone})
	sink.Event(composeclient.Event{Name: "3f2a", Status: "Error", Text: "denied", State: composeclient.EventError})
	sink.Event(composeclient.Event{Kind: "Volume", Name: "demo_kept", Text: "not recreated", State: composeclient.EventWarning})
	step.End(nil)

	// Assert
	type line struct {
		status   progress.Status
		msg      string
		resource progress.Resource
		settled  bool
	}
	var got []line
	for _, evt := range recorder.events {
		if evt.Resource != nil {
			got = append(got, line{evt.Status, evt.Msg, *evt.Resource, evt.Settled})
		}
	}
	assert.Equal(t, []line{
		{progress.StatusInProgress, "Stopping", progress.Resource{Kind: "container", ID: "demo-db-1"}, false},
		{progress.StatusInProgress, "Stopped", progress.Resource{Kind: "container", ID: "demo-db-1"}, true},
		{progress.StatusWarned, "Error denied", progress.Resource{Kind: composeResourceObject, ID: "3f2a"}, false},
		{progress.StatusWarned, "not recreated", progress.Resource{Kind: "volume", ID: "demo_kept"}, false},
	}, got)
}

func TestNewComposeEventSink_MuteNodeKeepsTheLog(t *testing.T) {
	// Act
	sink := newComposeEventSink(context.Background())

	// Assert
	assert.Nil(t, sink)
}
