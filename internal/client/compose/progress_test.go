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

// logRecords decodes what the adapter wrote, one map per zerolog record. The
// assertions go through the JSON rather than the rendered text so a case names
// the field it cares about instead of matching a line format.
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

// The events are the ones compose v5.6.1's own constructors build (pkg/compose/
// progress.go, pull.go), so the test doubles as the record of the shape arcave reads.
func TestProgressLogger_On(t *testing.T) {
	tests := []struct {
		name   string
		events []api.Resource
		verify func(t *testing.T, records []map[string]any)
	}{
		{
			name:   "a settled lifecycle event carries its object and lands on info",
			events: []api.Resource{{ID: "Container arcave-demo-minio-1", Status: api.Done, Text: api.StatusHealthy}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "info", records[0]["level"])
				assert.Equal(t, "compose", records[0]["message"])
				assert.Equal(t, "Container", records[0]["kind"])
				assert.Equal(t, "arcave-demo-minio-1", records[0]["name"])
				assert.Equal(t, "Healthy", records[0]["status"])
				assert.NotContains(t, records[0], "text", "a lifecycle event carries no details")
			},
		},
		{
			name:   "an in-flight event is demoted to debug",
			events: []api.Resource{{ID: "Container arcave-demo-app-1", Status: api.Working, Text: api.StatusStarting}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "debug", records[0]["level"])
				assert.Equal(t, "Starting", records[0]["status"])
			},
		},
		{
			name: "an error is reported at error level with its message",
			events: []api.Resource{{ID: "Container arcave-demo-app-1", Status: api.Error, Text: api.StatusError,
				Details: "port is already allocated"}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "error", records[0]["level"])
				assert.Equal(t, "port is already allocated", records[0]["text"])
			},
		},
		{
			name: "a skip is a warning and keeps its reason",
			events: []api.Resource{{ID: "Container arcave-demo-app-1", Status: api.Warning,
				Text: "Skipped: optional dependency is not running"}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "warn", records[0]["level"])
				assert.Equal(t, "Skipped: optional dependency is not running", records[0]["status"])
			},
		},
		{
			name: "a byte-progress tick is debug and carries percent",
			events: []api.Resource{{ID: "a2abf6c4d29d", Status: api.Working, Text: "Downloading", Details: "524.3kB",
				Current: 524288, Total: 1048576, Percent: 50}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "debug", records[0]["level"])
				assert.EqualValues(t, 50, records[0]["percent"])
				assert.Equal(t, "Downloading", records[0]["status"])
			},
		},
		{
			name:   "a completed layer has no total, so it stays visible at info",
			events: []api.Resource{{ID: "a2abf6c4d29d", Status: api.Done, Text: "Pull complete", Percent: 100}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "info", records[0]["level"])
				assert.NotContains(t, records[0], "percent", "percent is only meaningful while a total is known")
			},
		},
		{
			name: "two events in one call become two records",
			events: []api.Resource{
				{ID: "Container arcave-demo-app-1", Status: api.Working, Text: api.StatusStopping},
				{ID: "Container arcave-demo-postgres-1", Status: api.Done, Text: api.StatusStarted},
			},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 2)
				assert.Equal(t, "arcave-demo-app-1", records[0]["name"])
				assert.Equal(t, "arcave-demo-postgres-1", records[1]["name"])
			},
		},
		{
			name:   "an id with no space is all name",
			events: []api.Resource{{ID: "a2abf6c4d29d", Status: api.Done, Text: "Already exists"}},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "a2abf6c4d29d", records[0]["name"])
				assert.NotContains(t, records[0], "kind")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: debug level, so a case can assert a record was demoted
			// rather than dropped.
			buf := &bytes.Buffer{}
			p := NewProgressLogger(zerolog.New(buf).Level(zerolog.DebugLevel))

			// Act
			p.On(tt.events...)

			// Assert
			tt.verify(t, logRecords(t, buf))
		})
	}
}

// What compose writes to its error stream outside the event stream goes through
// line by line, verbatim.
func TestProgressLogger_Write(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		verify func(t *testing.T, records []map[string]any)
	}{
		{
			name:   "a line goes through verbatim, marked as compose's own",
			writes: []string{"No stopped containers\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "info", records[0]["level"])
				assert.Equal(t, "No stopped containers", records[0]["message"])
				assert.Equal(t, "compose", records[0]["source"])
			},
		},
		{
			name:   "two lines in one Write become two records",
			writes: []string{"first\nsecond\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 2)
				assert.Equal(t, "first", records[0]["message"])
				assert.Equal(t, "second", records[1]["message"])
			},
		},
		{
			name:   "a line split across two Writes becomes one record",
			writes: []string{"No stopped ", "containers\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "No stopped containers", records[0]["message"])
			},
		},
		{
			name:   "a blank line is dropped",
			writes: []string{"\n   \n"},
			verify: func(t *testing.T, records []map[string]any) {
				assert.Empty(t, records)
			},
		},
		{
			name:   "a stream with no newline is flushed once it outgrows the buffer bound",
			writes: []string{strings.Repeat("x", maxProgressLine+1)},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Len(t, records[0]["message"], maxProgressLine+1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			buf := &bytes.Buffer{}
			p := NewProgressLogger(zerolog.New(buf))

			// Act
			for _, write := range tt.writes {
				n, err := p.Write([]byte(write))
				require.NoError(t, err)
				assert.Equal(t, len(write), n)
			}

			// Assert
			tt.verify(t, logRecords(t, buf))
		})
	}
}

// A last line with no trailing newline would sit in the buffer forever, so Close
// hands it over — and having done so, has nothing left to hand over twice.
func TestProgressLogger_Close(t *testing.T) {
	// Arrange
	buf := &bytes.Buffer{}
	p := NewProgressLogger(zerolog.New(buf))
	_, err := p.Write([]byte("No stopped containers"))
	require.NoError(t, err)
	require.Empty(t, logRecords(t, buf), "an unterminated line is held, not emitted")

	// Act
	require.NoError(t, p.Close())
	require.NoError(t, p.Close())

	// Assert
	records := logRecords(t, buf)
	require.Len(t, records, 1)
	assert.Equal(t, "No stopped containers", records[0]["message"])
}

// The level mapping is what decides whether a run says anything at all, so a
// filtering logger must drop the in-flight events and keep the settled ones.
func TestProgressLogger_On_HonoursTheLoggerLevel(t *testing.T) {
	// Arrange
	buf := &bytes.Buffer{}
	p := NewProgressLogger(zerolog.New(buf).Level(zerolog.InfoLevel))

	// Act
	p.On(
		api.Resource{ID: "Container arcave-demo-app-1", Status: api.Working, Text: api.StatusStarting},
		api.Resource{ID: "Container arcave-demo-app-1", Status: api.Done, Text: api.StatusStarted},
	)

	// Assert
	records := logRecords(t, buf)
	require.Len(t, records, 1)
	assert.Equal(t, "Started", records[0]["status"])
}
