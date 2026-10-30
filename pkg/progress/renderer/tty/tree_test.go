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

package tty

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

func TestTree_apply(t *testing.T) {
	worker := func(id string) progress.Resource {
		return progress.Resource{Kind: "worker", ID: id}
	}
	tests := []struct {
		name   string
		events []progress.Event
		check  func(t *testing.T, got *tree)
	}{
		{
			name: "children in the order they open",
			events: newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
				ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
				exportCtx, export := progress.Begin(ctx, progress.Step{Kind: "album.export"})
				_, resize := progress.Run(exportCtx, progress.Step{Kind: "photo.resize"})
				_, upload := progress.Run(ctx, progress.Step{Kind: "cdn.upload"})
				resize.End(nil)
				export.End(nil)
				upload.End(nil)
				root.End(nil)
			}),
			check: func(t *testing.T, got *tree) {
				require.Contains(t, got.nodes, progress.StepID(1))
				require.Contains(t, got.nodes, progress.StepID(2))
				assert.Equal(t, []progress.StepID{1}, got.roots)
				assert.Equal(t, []progress.StepID{2, 4}, got.nodes[1].children)
				assert.Equal(t, []progress.StepID{3}, got.nodes[2].children)
			},
		},
		{
			name: "two roots of one tracker",
			events: newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
				_, first := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
				first.Warn("slow")
				_, second := tracker.Begin(ctx, progress.Step{Kind: "album.backup"})
				second.Warn("slow")
			}),
			check: func(t *testing.T, got *tree) {
				assert.Equal(t, []progress.StepID{1, 2}, got.roots)
			},
		},
		{
			name: "resources by first appearance",
			events: newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
				ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
				_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"}, progress.MeasureResources)
				pause.UpdateResource(worker("worker-1"), "Draining")
				pause.UpdateResource(worker("worker-2"), "Draining")
				pause.WarnResource(worker("worker-1"), "unreachable")
				pause.UpdateResource(worker("worker-2"), "Paused")
				root.Fail("", nil)
			}),
			check: func(t *testing.T, got *tree) {
				require.Contains(t, got.nodes, progress.StepID(2))
				assert.Equal(t, []resourceLine{
					{key: "worker/worker-1", resource: worker("worker-1"), msg: "unreachable", warned: true},
					{key: "worker/worker-2", resource: worker("worker-2"), msg: "Paused"},
				}, got.nodes[2].resources)
				assert.Empty(t, got.nodes[2].msg)
			},
		},
		{
			name: "warnings accumulate, marked as the result repeats them, and stay out of the message",
			events: newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
				ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
				_, file := progress.Run(ctx, progress.Step{Kind: "file"})
				file.Warn("retrying")
				file.WarnInResult("edge timeout")
				file.Done(progress.Sample{})
				root.End(nil)
			}),
			check: func(t *testing.T, got *tree) {
				require.Contains(t, got.nodes, progress.StepID(2))
				assert.Equal(t, []warning{
					{msg: "retrying"},
					{msg: "edge timeout", inResult: true},
				}, got.nodes[2].warnings)
				assert.Empty(t, got.nodes[2].msg)
			},
		},
		{
			name: "the message a failure sets",
			events: newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
				ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
				_, file := progress.Run(ctx, progress.Step{Kind: "file"})
				file.Update("uploading")
				file.Fail("unable to upload", errors.New("403 forbidden"))
				root.End(nil)
			}),
			check: func(t *testing.T, got *tree) {
				require.Contains(t, got.nodes, progress.StepID(2))
				assert.Equal(t, "unable to upload", got.nodes[2].msg)
				assert.Equal(t, "403 forbidden", got.nodes[2].last.Error)
			},
		},
		{
			name: "started and ended times",
			events: newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
				ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
				clock.advance(ms(300))
				_, file := progress.Run(ctx, progress.Step{Kind: "file"})
				clock.advance(ms(200))
				file.End(nil)
				root.End(nil)
			}),
			check: func(t *testing.T, got *tree) {
				require.Contains(t, got.nodes, progress.StepID(1))
				require.Contains(t, got.nodes, progress.StepID(2))
				assert.True(t, start.Equal(got.nodes[1].startedAt), "started at %v", got.nodes[1].startedAt)
				assert.True(t, start.Add(ms(300)).Equal(got.nodes[2].startedAt), "started at %v", got.nodes[2].startedAt)
				assert.True(t, start.Add(ms(500)).Equal(got.nodes[2].endedAt), "ended at %v", got.nodes[2].endedAt)
				assert.Equal(t, progress.StatusSucceeded, got.nodes[2].last.Status)
			},
		},
		{
			name: "a second started of a node is ignored",
			events: []progress.Event{
				{StepID: 1, Status: progress.StatusStarted, Open: &progress.Open{Step: progress.Step{Kind: "album.publish"}}, At: start},
				{StepID: 2, Status: progress.StatusStarted, Open: &progress.Open{Parent: 1, Step: progress.Step{Kind: "file"}}, At: start},
				{StepID: 2, Status: progress.StatusStarted, Open: &progress.Open{Parent: 1, Step: progress.Step{Kind: "file"}}, At: start},
			},
			check: func(t *testing.T, got *tree) {
				require.Contains(t, got.nodes, progress.StepID(1))
				assert.Equal(t, []progress.StepID{1}, got.roots)
				assert.Equal(t, []progress.StepID{2}, got.nodes[1].children)
			},
		},
		{
			name: "an event without its started is dropped",
			events: []progress.Event{
				{StepID: 2, Status: progress.StatusInProgress, Msg: "orphan", At: start},
				{
					StepID: 3,
					Status: progress.StatusStarted,
					Open:   &progress.Open{Parent: 2, Step: progress.Step{Kind: "file"}},
					At:     start,
				},
			},
			check: func(t *testing.T, got *tree) {
				assert.Empty(t, got.nodes)
				assert.Empty(t, got.roots)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newTree()

			for _, evt := range tt.events {
				got.apply(evt)
			}

			tt.check(t, got)
		})
	}
}

func TestTree_snapshot(t *testing.T) {
	worker := progress.Resource{Kind: "worker", ID: "worker-1"}
	original := newTreeOf(newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
		ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
		root.Warn("slow")
		_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"}, progress.MeasureResources)
		pause.UpdateResource(worker, "Draining")
	}))

	snapshot := original.snapshot()
	original.apply(progress.Event{StepID: 1, Status: progress.StatusWarned, Msg: "slower", At: start})
	original.apply(progress.Event{StepID: 2, Status: progress.StatusInProgress, Msg: "Paused", Resource: &worker, At: start})
	original.apply(progress.Event{
		StepID: 3,
		Status: progress.StatusStarted,
		Open:   &progress.Open{Parent: 1, Step: progress.Step{Kind: "file"}},
		At:     start,
	})

	require.Contains(t, snapshot.nodes, progress.StepID(1))
	require.Contains(t, snapshot.nodes, progress.StepID(2))
	require.Len(t, snapshot.nodes[2].resources, 1)
	assert.Equal(t, []warning{{msg: "slow"}}, snapshot.nodes[1].warnings)
	assert.Equal(t, []progress.StepID{2}, snapshot.nodes[1].children)
	assert.Equal(t, "Draining", snapshot.nodes[2].resources[0].msg)
	assert.NotContains(t, snapshot.nodes, progress.StepID(3))
}
