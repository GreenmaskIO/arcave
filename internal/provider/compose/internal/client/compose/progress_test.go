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
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logRecords decodes what the processor wrote, one map per zerolog record.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record), "log line %q", line)
		records = append(records, record)
	}
	return records
}

func TestEventLogger_On(t *testing.T) {
	tests := []struct {
		name     string
		resource api.Resource
		want     map[string]any
		absent   []string
	}{
		{
			name:     "a settled event carries its object and lands on info",
			resource: api.Resource{ID: "Container arcave-demo-minio-1", Text: api.StatusHealthy, Status: api.Done},
			want: map[string]any{
				"level": "info", "message": "compose", "kind": "Container",
				"name": "arcave-demo-minio-1", "status": "Healthy",
			},
			absent: []string{"text", "percent"},
		},
		{
			name:     "an in-flight event is demoted to debug",
			resource: api.Resource{ID: "Container arcave-demo-app-1", Text: api.StatusStarting, Status: api.Working},
			want:     map[string]any{"level": "debug", "status": "Starting"},
		},
		{
			name:     "a warning is reported at warn",
			resource: api.Resource{ID: "Volume demo_data", Text: "Skipped", Details: "in use", Status: api.Warning},
			want:     map[string]any{"level": "warn", "kind": "Volume", "status": "Skipped", "text": "in use"},
		},
		{
			name:     "an error is reported at error",
			resource: api.Resource{ID: "Network demo_default", Text: api.StatusError, Details: "in use", Status: api.Error},
			want:     map[string]any{"level": "error", "status": "Error", "text": "in use"},
		},
		{
			name: "byte progress carries its percent",
			resource: api.Resource{
				ID: "Image postgres:16", Text: api.StatusDownloading, Status: api.Working, Total: 100, Percent: 40,
			},
			want: map[string]any{"level": "debug", "percent": float64(40)},
		},
		{
			name:     "an id with no space is all name",
			resource: api.Resource{ID: "demo", Text: api.StatusCreated, Status: api.Done},
			want:     map[string]any{"name": "demo"},
			absent:   []string{"kind"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			buf := &bytes.Buffer{}
			p := NewEventLogger(zerolog.New(buf).Level(zerolog.DebugLevel))

			// Act
			p.On(tt.resource)

			// Assert
			records := logRecords(t, buf)
			require.Len(t, records, 1)
			for key, value := range tt.want {
				assert.Equal(t, value, records[0][key], key)
			}
			for _, key := range tt.absent {
				assert.NotContains(t, records[0], key)
			}
		})
	}
}

func TestNewEventState(t *testing.T) {
	tests := []struct {
		name   string
		status api.EventStatus
		want   EventState
	}{
		{name: "working", status: api.Working, want: EventWorking},
		{name: "done", status: api.Done, want: EventDone},
		{name: "warning", status: api.Warning, want: EventWarning},
		{name: "error", status: api.Error, want: EventError},
		{name: "unknown reads as compose renders it, an error", status: api.EventStatus(42), want: EventError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := newEventState(api.Resource{Status: tt.status})

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The level table decides whether a run says anything at all: a filtering logger drops the
// in-flight events and keeps the settled ones.
func TestEventLogger_On_HonoursTheLoggerLevel(t *testing.T) {
	// Arrange
	buf := &bytes.Buffer{}
	p := NewEventLogger(zerolog.New(buf).Level(zerolog.InfoLevel))

	// Act
	p.On(
		api.Resource{ID: "Container arcave-demo-app-1", Text: api.StatusStarting, Status: api.Working},
		api.Resource{ID: "Container arcave-demo-app-1", Text: api.StatusStarted, Status: api.Done},
	)

	// Assert
	records := logRecords(t, buf)
	require.Len(t, records, 1)
	assert.Equal(t, "Started", records[0]["status"])
}

// sinkRecorder keeps every event it is handed.
type sinkRecorder struct {
	events []Event
}

func (r *sinkRecorder) Event(evt Event) {
	r.events = append(r.events, evt)
}

func TestEventLogger_On_ToTheSink(t *testing.T) {
	// Arrange
	var buf bytes.Buffer
	p := NewEventLogger(zerolog.New(&buf))
	sink := &sinkRecorder{}
	p.SetSink(sink)

	// Act
	p.On(
		api.Resource{ID: "Container arcave-demo-db-1", Text: api.StatusStopping, Status: api.Working},
		api.Resource{ID: "Container arcave-demo-db-1", Text: api.StatusStopped, Status: api.Done},
		api.Resource{ID: "Network arcave-demo_default", Text: api.StatusError, Details: "in use", Status: api.Error},
	)
	p.SetSink(nil)
	p.On(api.Resource{ID: "Container arcave-demo-db-1", Text: api.StatusRemoved, Status: api.Done})

	// Assert
	assert.Equal(t, []Event{
		{Kind: "Container", Name: "arcave-demo-db-1", Status: "Stopping", State: EventWorking},
		{Kind: "Container", Name: "arcave-demo-db-1", Status: "Stopped", State: EventDone},
		{Kind: "Network", Name: "arcave-demo_default", Status: "Error", Text: "in use", State: EventError},
	}, sink.events)
	records := logRecords(t, &buf)
	require.Len(t, records, 1, "only what came after the sink was taken back is logged")
	assert.Equal(t, "Removed", records[0]["status"])
}
