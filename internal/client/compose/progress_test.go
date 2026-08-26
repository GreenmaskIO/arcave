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

// The golden inputs are lines captured from compose v2.40.3 running the demo
// stack, not invented ones: the test doubles as the record of the schema arcave
// decodes, and is what fails first when the pinned compose version changes it.
func TestProgressLogger_Write(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		verify func(t *testing.T, records []map[string]any)
	}{
		{
			name:   "a settled lifecycle event carries its object and lands on info",
			writes: []string{`{"id":"Container arcave-demo-minio-1","status":"Healthy"}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "info", records[0]["level"])
				assert.Equal(t, "compose", records[0]["message"])
				assert.Equal(t, "Container", records[0]["kind"])
				assert.Equal(t, "arcave-demo-minio-1", records[0]["name"])
				assert.Equal(t, "Healthy", records[0]["status"])
			},
		},
		{
			name:   "an in-flight event is demoted to debug",
			writes: []string{`{"id":"Container arcave-demo-app-1","status":"Starting"}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "debug", records[0]["level"])
				assert.Equal(t, "Starting", records[0]["status"])
			},
		},
		{
			name:   "Error is reported at error level",
			writes: []string{`{"id":"Container arcave-demo-app-1","status":"Error"}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "error", records[0]["level"])
			},
		},
		{
			name:   "a skip is a warning and keeps its reason",
			writes: []string{`{"id":"Container arcave-demo-app-1","status":"Skipped: optional dependency is not running"}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "warn", records[0]["level"])
				assert.Equal(t, "Skipped: optional dependency is not running", records[0]["status"])
			},
		},
		{
			name:   "a byte-progress tick is debug and carries percent",
			writes: []string{`{"id":"a2abf6c4d29d","text":"Downloading","status":"Downloading","current":524288,"total":1048576,"percent":50}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "debug", records[0]["level"])
				assert.EqualValues(t, 50, records[0]["percent"])
				assert.Equal(t, "Downloading", records[0]["text"])
			},
		},
		{
			name:   "a completed layer has no total, so it stays visible at info",
			writes: []string{`{"id":"a2abf6c4d29d","text":"Pull complete","status":"Pull complete","percent":100}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "info", records[0]["level"])
				assert.NotContains(t, records[0], "percent", "percent is only meaningful while a total is known")
			},
		},
		{
			name:   "a line compose wrote outside its event stream goes through verbatim",
			writes: []string{"Warning: memory limits are not supported\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "info", records[0]["level"])
				assert.Equal(t, "Warning: memory limits are not supported", records[0]["message"])
				assert.Equal(t, "compose", records[0]["source"])
			},
		},
		{
			name:   "a line that starts like JSON but does not decode goes through verbatim",
			writes: []string{"{not json\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "{not json", records[0]["message"])
			},
		},
		{
			name: "two events in one Write become two records",
			writes: []string{
				`{"id":"Container arcave-demo-app-1","status":"Stopping"}` + "\n" +
					`{"id":"Container arcave-demo-postgres-1","status":"Started"}` + "\n",
			},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 2)
				assert.Equal(t, "arcave-demo-app-1", records[0]["name"])
				assert.Equal(t, "arcave-demo-postgres-1", records[1]["name"])
			},
		},
		{
			name: "an event split across two Writes becomes one record",
			writes: []string{
				`{"id":"Container arcave-demo-app-1",`,
				`"status":"Healthy"}` + "\n",
			},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "Healthy", records[0]["status"])
			},
		},
		{
			name:   "an id with no space is all name",
			writes: []string{`{"id":"a2abf6c4d29d","status":"Already exists"}` + "\n"},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "a2abf6c4d29d", records[0]["name"])
				assert.NotContains(t, records[0], "kind")
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
			// Arrange: debug level, so a case can assert a record was demoted
			// rather than dropped.
			buf := &bytes.Buffer{}
			p := NewProgressLogger(zerolog.New(buf).Level(zerolog.DebugLevel))

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
	_, err := p.Write([]byte(`{"id":"Container arcave-demo-app-1","status":"Started"}`))
	require.NoError(t, err)
	require.Empty(t, logRecords(t, buf), "an unterminated line is held, not emitted")

	// Act
	require.NoError(t, p.Close())
	require.NoError(t, p.Close())

	// Assert
	records := logRecords(t, buf)
	require.Len(t, records, 1)
	assert.Equal(t, "arcave-demo-app-1", records[0]["name"])
}

// The level table is what decides whether a run says anything at all, so a
// filtering logger must drop the in-flight events and keep the settled ones.
func TestProgressLogger_Write_HonoursTheLoggerLevel(t *testing.T) {
	// Arrange
	buf := &bytes.Buffer{}
	p := NewProgressLogger(zerolog.New(buf).Level(zerolog.InfoLevel))

	// Act
	_, err := p.Write([]byte(
		`{"id":"Container arcave-demo-app-1","status":"Starting"}` + "\n" +
			`{"id":"Container arcave-demo-app-1","status":"Started"}` + "\n"))
	require.NoError(t, err)

	// Assert
	records := logRecords(t, buf)
	require.Len(t, records, 1)
	assert.Equal(t, "Started", records[0]["status"])
}
