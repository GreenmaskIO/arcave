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

package reporter

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

var start = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// newStream is a stream exercising every line rule: a batch, a byte tick, a resource, a
// warning, a failure with its error, a cancellation and unknown or exceeded totals.
func newStream() []progress.Event {
	at := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	elapsed := func(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
	return []progress.Event{
		{
			Node:   1,
			Status: progress.StatusStarted,
			Open:   &progress.Open{Step: progress.Step{Kind: "album.backup", Type: progress.NodeTypeWorkflow, Name: "summer"}},
			At:     at(0),
		},
		{
			Node:   2,
			Status: progress.StatusStarted,
			Open: &progress.Open{
				Parent:   1,
				Step:     progress.Step{Kind: "queue.pause", Type: progress.NodeTypeActivity},
				Measures: []progress.Measure{progress.MeasureResources},
			},
			At: at(0),
		},
		{
			Node:     2,
			Status:   progress.StatusInProgress,
			Msg:      "Paused",
			Resource: &progress.Resource{Kind: "worker", ID: "worker-1"},
			Settled:  true,
			Sample:   progress.Sample{Elapsed: elapsed(100)},
			At:       at(100),
		},
		{
			Node:     2,
			Status:   progress.StatusWarned,
			Msg:      "unreachable",
			Resource: &progress.Resource{Kind: "worker", ID: "worker-2"},
			Sample:   progress.Sample{Elapsed: elapsed(200)},
			At:       at(200),
		},
		{
			Node:   2,
			Status: progress.StatusSucceeded,
			Sample: progress.Sample{Elapsed: elapsed(300)},
			At:     at(300),
		},
		{
			Node:   3,
			Status: progress.StatusStarted,
			Open: &progress.Open{
				Parent:   1,
				Step:     progress.Step{Kind: "cdn.upload", Type: progress.NodeTypeWorkflow},
				Measures: []progress.Measure{progress.MeasureItems},
			},
			Sample: progress.Sample{Items: &progress.Items{Total: 3}},
			At:     at(300),
		},
		{
			Node:   4,
			Status: progress.StatusStarted,
			Open: &progress.Open{
				Parent:   3,
				Step:     progress.Step{Kind: "file", Type: progress.NodeTypeActivity, ID: "beach.jpg"},
				Measures: []progress.Measure{progress.MeasureBytes},
			},
			At: at(300),
		},
		{
			Node:   4,
			Status: progress.StatusInProgress,
			Sample: progress.Sample{Elapsed: elapsed(100), Bytes: &progress.Bytes{Done: 1_050_000}},
			At:     at(400),
		},
		{
			Node:   4,
			Status: progress.StatusInProgress,
			Msg:    "retrying",
			Sample: progress.Sample{Elapsed: elapsed(200), Bytes: &progress.Bytes{Done: 1_050_000}},
			At:     at(500),
		},
		{
			Node:   4,
			Status: progress.StatusSucceeded,
			Msg:    "retrying",
			Sample: progress.Sample{Elapsed: elapsed(300), Bytes: &progress.Bytes{Done: 2_500_000, Total: 2_100_000, Stored: 400_000, Reused: 1_700_000}},
			At:     at(600),
		},
		{
			Node:   3,
			Status: progress.StatusInProgress,
			Sample: progress.Sample{Elapsed: elapsed(300), Items: &progress.Items{Done: 1, Total: 3}},
			At:     at(600),
		},
		{
			Node:   5,
			Status: progress.StatusStarted,
			Open: &progress.Open{
				Parent:   3,
				Step:     progress.Step{Kind: "file", Type: progress.NodeTypeActivity, ID: "raw.dng"},
				Measures: []progress.Measure{progress.MeasureBytes},
			},
			At: at(600),
		},
		{
			Node:   5,
			Status: progress.StatusFailed,
			Msg:    "unable to upload",
			Error:  "403 forbidden",
			Sample: progress.Sample{Elapsed: elapsed(100), Bytes: &progress.Bytes{Done: 200_000, Total: 400_000}},
			At:     at(700),
		},
		{
			Node:   3,
			Status: progress.StatusInProgress,
			Sample: progress.Sample{Elapsed: elapsed(400), Items: &progress.Items{Done: 1, Failed: 1, Total: 3}},
			At:     at(700),
		},
		{
			Node:   6,
			Status: progress.StatusStarted,
			Open: &progress.Open{
				Parent: 3,
				Step:   progress.Step{Kind: "file", Type: progress.NodeTypeActivity, ID: "dunes.jpg"},
			},
			At: at(700),
		},
		{
			Node:   6,
			Status: progress.StatusCancelled,
			At:     at(700),
		},
		{
			Node:   3,
			Status: progress.StatusFailed,
			Sample: progress.Sample{Elapsed: elapsed(400), Items: &progress.Items{Done: 1, Failed: 1, Cancelled: 1, Total: 3}},
			At:     at(700),
		},
		{
			Node:   7,
			Status: progress.StatusStarted,
			Open: &progress.Open{
				Parent:   1,
				Step:     progress.Step{Kind: "cdn.purge", Type: progress.NodeTypeWorkflow},
				Measures: []progress.Measure{progress.MeasureItems},
			},
			Sample: progress.Sample{Items: &progress.Items{}},
			At:     at(700),
		},
		{
			Node:   7,
			Status: progress.StatusSucceeded,
			Sample: progress.Sample{Items: &progress.Items{Done: 2}},
			At:     at(700),
		},
		{
			Node:   1,
			Status: progress.StatusFailed,
			Sample: progress.Sample{Elapsed: elapsed(1234)},
			At:     at(1234),
		},
	}
}

// assertGolden compares got with testdata/name, rewriting it under -update.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}
