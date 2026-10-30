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

package progress_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// recorder keeps every event it is handed.
type recorder struct {
	events []progress.Event
}

func (r *recorder) Report(evt progress.Event) {
	r.events = append(r.events, evt)
}

// lines renders each event as one comparable line.
func (r *recorder) lines() []string {
	lines := make([]string, 0, len(r.events))
	for _, evt := range r.events {
		lines = append(lines, formatEvent(evt))
	}
	return lines
}

func formatEvent(evt progress.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s", evt.Node, evt.Status)
	if evt.Open != nil {
		fmt.Fprintf(&b, " <%d %s %s", evt.Open.Parent, evt.Open.Step.Kind, evt.Open.Step.Type)
		if len(evt.Open.Measures) > 0 {
			fmt.Fprintf(&b, " %v", evt.Open.Measures)
		}
	}
	if evt.Msg != "" {
		fmt.Fprintf(&b, " msg=%q", evt.Msg)
	}
	if evt.Error != "" {
		fmt.Fprintf(&b, " error=%q", evt.Error)
	}
	if evt.Resource != nil {
		fmt.Fprintf(&b, " resource=%s:%s", evt.Resource.Kind, evt.Resource.ID)
	}
	if evt.Settled {
		b.WriteString(" settled")
	}
	if evt.InResult {
		b.WriteString(" in_result")
	}
	if evt.Sample.Bytes != nil {
		fmt.Fprintf(&b, " bytes=%d/%d", evt.Sample.Bytes.Done, evt.Sample.Bytes.Total)
	}
	if items := evt.Sample.Items; items != nil {
		fmt.Fprintf(&b, " items=%d/%d/%d/%d", items.Done, items.Failed, items.Cancelled, items.Total)
	}
	return b.String()
}

func newTestTracker() (*progress.Tracker, *recorder, *exampleClock) {
	var (
		events = &recorder{}
		clock  = &exampleClock{now: start}
	)
	return progress.New(events, clock.Now), events, clock
}

func newLoggedContext(buf *bytes.Buffer) context.Context {
	return zerolog.New(buf).WithContext(context.Background())
}

var (
	stepPublish = progress.Step{Kind: kindAlbumPublish}
	stepExport  = progress.Step{Kind: kindAlbumExport}
	stepExtras  = progress.Step{Kind: kindAlbumExtras}
	stepResize  = progress.Step{Kind: kindPhotoResize}
	stepPause   = progress.Step{Kind: kindQueuePause}
	stepWrite   = progress.Step{Kind: kindManifestWrite}
)

func newStepPhoto(name string) progress.Step {
	return progress.Step{Kind: kindPhoto, ID: name}
}

func TestTracker_Begin(t *testing.T) {
	var (
		errQueue    = errors.New("queue unreachable")
		errLockLost = errors.New("session lock lost")
	)
	tests := []struct {
		name string
		run  func(ctx context.Context, tracker *progress.Tracker, clock *exampleClock)
		want []string
	}{
		{
			name: "an empty workflow that succeeds reports nothing",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				_, extras := progress.Begin(ctx, stepExtras)
				extras.End(nil)
				root.End(nil)
			},
			want: []string{},
		},
		{
			name: "an empty workflow that fails opens its ancestors and carries its own error",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				_, extras := progress.Begin(ctx, stepExtras)
				extras.End(errQueue)
				root.End(errQueue)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 album.extras workflow",
				`2 failed error="queue unreachable"`,
				"1 failed",
			},
		},
		{
			name: "a child opens its lazy ancestors top down, numbered in order",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				ctx, export := progress.Begin(ctx, stepExport)
				_, write := progress.Run(ctx, stepWrite)
				write.End(nil)
				export.End(nil)
				root.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 album.export workflow",
				"3 started <2 manifest.write activity",
				"3 succeeded",
				"2 succeeded",
				"1 succeeded",
			},
		},
		{
			name: "an update keeps a lazy workflow unopened",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				_, root := tracker.Begin(ctx, stepPublish)
				root.Update("resizing")
				root.Progress(progress.Sample{})
				root.End(nil)
			},
			want: []string{},
		},
		{
			name: "a batch counts a lazy child that ended unopened",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, batch := progress.Batch(withRoot(ctx, tracker), stepResize, 1)
				_, extras := progress.Begin(ctx, stepExtras)
				extras.End(nil)
				batch.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 photo.resize workflow [items] items=0/0/0/1",
				"2 in_progress items=1/0/0/1",
				"2 succeeded items=1/0/0/1",
			},
		},
		{
			name: "a child ending after its batch leaves the batch's count alone",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, batch := progress.Batch(withRoot(ctx, tracker), stepResize, 1)
				_, beach := progress.Run(ctx, newStepPhoto("beach.jpg"))
				batch.End(nil)
				beach.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 photo.resize workflow [items] items=0/0/0/1",
				"3 started <2 photo activity",
				"2 succeeded items=0/0/0/1",
				"3 succeeded",
			},
		},
		{
			name: "a warning opens a lazy workflow, and its message stays its event's",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				_, root := tracker.Begin(ctx, stepPublish)
				root.Warn("2 raw files skipped")
				root.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				`1 warned msg="2 raw files skipped"`,
				"1 succeeded",
			},
		},
		{
			name: "a warning the result repeats is marked so",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				_, root := tracker.Begin(ctx, stepPublish)
				root.WarnInResult("2 raw files skipped")
				root.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				`1 warned msg="2 raw files skipped" in_result`,
				"1 succeeded",
			},
		},
		{
			name: "the constructor sets the type, whatever the caller passed",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, progress.Step{Kind: kindAlbumPublish, Type: progress.NodeTypeActivity})
				_, write := progress.Run(ctx, progress.Step{Kind: kindManifestWrite, Type: progress.NodeTypeWorkflow})
				write.End(nil)
				root.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 manifest.write activity",
				"2 succeeded",
				"1 succeeded",
			},
		},
		{
			name: "a batch opens at once and counts its children's outcomes",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				ctx, batch := progress.Batch(ctx, stepResize, 3)
				_, beach := progress.Run(ctx, newStepPhoto("beach.jpg"), progress.MeasureBytes)
				beach.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 1, Total: 2}})
				beach.End(nil)
				_, dunes := progress.Run(ctx, newStepPhoto("dunes.jpg"))
				dunes.Fail("unable to resize", errQueue)
				_, sunset := progress.Run(ctx, newStepPhoto("sunset.jpg"))
				sunset.End(context.Canceled)
				batch.End(errQueue)
				root.End(errQueue)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 photo.resize workflow [items] items=0/0/0/3",
				"3 started <2 photo activity [bytes]",
				"3 in_progress bytes=1/2",
				"3 succeeded bytes=1/2",
				"2 in_progress items=1/0/0/3",
				"4 started <2 photo activity",
				`4 failed msg="unable to resize" error="queue unreachable"`,
				"2 in_progress items=1/1/0/3",
				"5 started <2 photo activity",
				"5 cancelled",
				"2 in_progress items=1/1/1/3",
				"2 failed items=1/1/1/3",
				"1 failed",
			},
		},
		{
			name: "a cancellation, wrapped or not, is no failure and carries no error",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				_, pause := progress.Run(ctx, stepPause)
				pause.Fail("", fmt.Errorf("pause: %w", context.Canceled))
				root.End(context.Canceled)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity",
				"2 cancelled",
				"1 cancelled",
			},
		},
		{
			name: "a deadline is a failure",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				_, root := tracker.Begin(ctx, stepPublish)
				root.End(context.DeadlineExceeded)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				`1 failed error="context deadline exceeded"`,
			},
		},
		{
			name: "a child opened under a context cancelled with a cause ends cancelled on that cause",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				lockCtx, cancel := context.WithCancelCause(ctx)
				_, pause := progress.Run(lockCtx, stepPause)
				_, resize := progress.Run(lockCtx, newStepPhoto("beach.jpg"))
				cancel(errLockLost)
				pause.End(fmt.Errorf("interrupted: %w", context.Cause(lockCtx)))
				resize.End(lockCtx.Err())
				root.End(fmt.Errorf("interrupted: %w", errLockLost))
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity",
				"3 started <1 photo activity",
				"2 cancelled",
				"3 cancelled",
				`1 failed error="interrupted: session lock lost"`,
			},
		},
		{
			name: "a deadline its context ran out on is still a failure",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				deadlineCtx, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				_, pause := progress.Run(deadlineCtx, stepPause)
				pause.End(deadlineCtx.Err())
				root.End(deadlineCtx.Err())
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity",
				`2 failed error="context deadline exceeded"`,
				"1 failed",
			},
		},
		{
			name: "an error unrelated to its context's cause is a failure",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				lockCtx, cancel := context.WithCancelCause(ctx)
				_, pause := progress.Run(lockCtx, stepPause)
				cancel(errLockLost)
				pause.End(errQueue)
				root.End(errQueue)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity",
				`2 failed error="queue unreachable"`,
				"1 failed",
			},
		},
		{
			name: "a workflow under a cancelled child still carries its own error",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx, root := tracker.Begin(ctx, stepPublish)
				_, pause := progress.Run(ctx, stepPause)
				pause.End(context.Canceled)
				root.End(errQueue)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity",
				"2 cancelled",
				`1 failed error="queue unreachable"`,
			},
		},
		{
			name: "a node ends once and ignores every call after",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				_, pause := progress.Run(withRoot(ctx, tracker), stepPause, progress.MeasureResources)
				pause.Done(progress.Sample{})
				pause.Fail("late", errQueue)
				pause.Update("late")
				pause.Warn("late")
				pause.UpdateResource(progress.Resource{Kind: resourceWorker, ID: "worker-1"}, "late")
				pause.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity [resources]",
				"2 succeeded",
			},
		},
		{
			name: "every event is a full snapshot: Done replaces only what it sets, Fail keeps the last sample",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				ctx = withRoot(ctx, tracker)
				_, beach := progress.Run(ctx, newStepPhoto("beach.jpg"), progress.MeasureBytes, progress.MeasureItems)
				beach.Update("resizing")
				beach.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 1, Total: 4}})
				beach.Progress(progress.Sample{Items: &progress.Items{Done: 1}})
				beach.Done(progress.Sample{Items: &progress.Items{Done: 2}})
				_, dunes := progress.Run(ctx, newStepPhoto("dunes.jpg"), progress.MeasureBytes)
				dunes.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 3, Total: 4}})
				dunes.Fail("", errQueue)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 photo activity [bytes items]",
				`2 in_progress msg="resizing"`,
				`2 in_progress msg="resizing" bytes=1/4`,
				`2 in_progress msg="resizing" bytes=1/4 items=1/0/0/0`,
				`2 succeeded msg="resizing" bytes=1/4 items=2/0/0/0`,
				"3 started <1 photo activity [bytes]",
				"3 in_progress bytes=3/4",
				`3 failed error="queue unreachable" bytes=3/4`,
			},
		},
		{
			name: "a summary activity reports each resource it declared",
			run: func(ctx context.Context, tracker *progress.Tracker, _ *exampleClock) {
				_, pause := progress.Run(withRoot(ctx, tracker), stepPause, progress.MeasureResources)
				pause.UpdateResource(progress.Resource{Kind: resourceWorker, ID: "worker-1"}, "Draining")
				pause.SettleResource(progress.Resource{Kind: resourceWorker, ID: "worker-1"}, "Paused")
				pause.WarnResource(progress.Resource{Kind: resourceWorker, ID: "worker-2"}, "unreachable")
				pause.End(nil)
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 queue.pause activity [resources]",
				`2 in_progress msg="Draining" resource=worker:worker-1`,
				`2 in_progress msg="Paused" resource=worker:worker-1 settled`,
				`2 warned msg="unreachable" resource=worker:worker-2`,
				"2 succeeded",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker, events, clock := newTestTracker()

			tt.run(context.Background(), tracker, clock)

			assert.Equal(t, tt.want, events.lines())
		})
	}
}

// withRoot opens a root under ctx; the first activity under it opens it.
func withRoot(ctx context.Context, tracker *progress.Tracker) context.Context {
	ctx, _ = tracker.Begin(ctx, stepPublish)
	return ctx
}

func TestTracker_Begin_ElapsedCountsFromTheConstructor(t *testing.T) {
	tracker, events, clock := newTestTracker()

	ctx, root := tracker.Begin(context.Background(), stepPublish)
	clock.advance(time.Second)
	_, write := progress.Run(ctx, stepWrite)
	clock.advance(time.Second)
	write.End(nil)
	root.End(nil)

	require.Len(t, events.events, 4)
	assert.Equal(t, time.Second, events.events[0].Sample.Elapsed, "a lazy workflow counts from its constructor")
	assert.Equal(t, time.Duration(0), events.events[1].Sample.Elapsed)
	assert.Equal(t, time.Second, events.events[2].Sample.Elapsed)
	assert.Equal(t, 2*time.Second, events.events[3].Sample.Elapsed)
	assert.True(t, events.events[3].At.Equal(start.Add(2*time.Second)), "at = %v", events.events[3].At)
}

func TestNode_End_DeferredOverANamedError(t *testing.T) {
	tracker, events, _ := newTestTracker()
	errQueue := errors.New("queue unreachable")
	pause := func(ctx context.Context) (err error) {
		_, node := progress.Run(ctx, stepPause)
		defer func() { node.End(err) }()
		return errQueue
	}

	err := pause(withRoot(context.Background(), tracker))

	require.ErrorIs(t, err, errQueue)
	assert.Equal(t, []string{
		"1 started <0 album.publish workflow",
		"2 started <1 queue.pause activity",
		`2 failed error="queue unreachable"`,
	}, events.lines())
}

func TestNode_Progress(t *testing.T) {
	tests := []struct {
		name        string
		run         func(ctx context.Context)
		want        []string
		wantDropped []progress.Measure
	}{
		{
			name: "bytes and items on a spinner",
			run: func(ctx context.Context) {
				_, write := progress.Run(ctx, stepWrite)
				write.Progress(progress.Sample{
					Bytes: &progress.Bytes{Done: 1},
					Items: &progress.Items{Done: 1},
				})
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 manifest.write activity",
				"2 in_progress",
			},
			wantDropped: []progress.Measure{progress.MeasureBytes, progress.MeasureItems},
		},
		{
			name: "a resource on a node not declaring resources, dropped with its message",
			run: func(ctx context.Context) {
				_, write := progress.Run(ctx, stepWrite)
				write.UpdateResource(progress.Resource{Kind: resourceWorker, ID: "worker-1"}, "Paused")
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 manifest.write activity",
			},
			wantDropped: []progress.Measure{progress.MeasureResources},
		},
		{
			name: "items on a batch, which counts them itself",
			run: func(ctx context.Context) {
				_, batch := progress.Batch(ctx, stepResize, 2)
				batch.Done(progress.Sample{Items: &progress.Items{Done: 2, Total: 2}})
			},
			want: []string{
				"1 started <0 album.publish workflow",
				"2 started <1 photo.resize workflow [items] items=0/0/0/2",
				"2 succeeded items=0/0/0/2",
			},
			wantDropped: []progress.Measure{progress.MeasureItems},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			tracker, events, _ := newTestTracker()
			ctx := withRoot(newLoggedContext(&logs), tracker)

			tt.run(ctx)

			assert.Equal(t, tt.want, events.lines())
			assert.Equal(t, len(tt.wantDropped), strings.Count(logs.String(), "the node does not report this measure"))
			for _, measure := range tt.wantDropped {
				assert.Contains(t, logs.String(), `"measure":"`+string(measure)+`"`)
			}
		})
	}
}

func TestRun_UnderAnActivity(t *testing.T) {
	var logs bytes.Buffer
	tracker, events, _ := newTestTracker()
	ctx, write := progress.Run(withRoot(newLoggedContext(&logs), tracker), stepWrite)

	childCtx, child := progress.Run(ctx, stepPause)
	child.Update("never reported")
	child.End(nil)
	_, batch := progress.Batch(ctx, stepResize, 1)
	batch.End(nil)
	_, export := progress.Begin(ctx, stepExport)
	export.Warn("never reported")
	_, grandchild := progress.Run(childCtx, stepWrite)
	grandchild.End(nil)
	write.End(nil)

	assert.Same(t, write, progress.Current(childCtx), "the context comes back unchanged")
	assert.Equal(t, []string{
		"1 started <0 album.publish workflow",
		"2 started <1 manifest.write activity",
		"2 succeeded",
	}, events.lines())
	assert.Contains(t, logs.String(), "progress: an activity has no children, the node is mute")
}

func TestCurrent(t *testing.T) {
	tracker, events, _ := newTestTracker()

	ctx, write := progress.Run(withRoot(context.Background(), tracker), stepWrite)
	progress.Current(ctx).Update("from deep down")

	assert.Same(t, write, progress.Current(ctx))
	assert.Equal(t, []string{
		"1 started <0 album.publish workflow",
		"2 started <1 manifest.write activity",
		`2 in_progress msg="from deep down"`,
	}, events.lines())
}

func TestBegin(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
	}{
		{
			name: "no tracker in the context",
			ctx:  context.Background,
		},
		{
			name: "a discarding tracker",
			ctx: func() context.Context {
				ctx, _ := progress.Discard().Begin(context.Background(), stepPublish)
				return ctx
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx()
			mute := progress.Current(ctx)

			exportCtx, export := progress.Begin(ctx, stepExport)
			export.Warn("unseen")
			batchCtx, batch := progress.Batch(exportCtx, stepResize, 1)
			beachCtx, beach := progress.Run(batchCtx, newStepPhoto("beach.jpg"), progress.MeasureBytes)
			beach.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 1}})
			beach.Fail("unseen", errors.New("unseen"))
			batch.End(nil)
			export.End(nil)

			assert.Equal(t, ctx, exportCtx)
			assert.Equal(t, ctx, batchCtx)
			assert.Equal(t, ctx, beachCtx)
			assert.Same(t, mute, export)
			assert.Same(t, mute, batch)
			assert.Same(t, mute, beach)
		})
	}
}

func TestWithTracker(t *testing.T) {
	tracker, events, _ := newTestTracker()
	ctx := progress.WithTracker(context.Background(), tracker)

	rootCtx, root := progress.Begin(ctx, stepPublish)
	_, export := progress.Begin(rootCtx, stepExport)
	export.Warn("2 raw files skipped")
	export.End(nil)
	root.End(nil)
	_, next := progress.Begin(ctx, stepExtras)
	next.Warn("a second root")
	next.End(nil)

	assert.Equal(t, []string{
		"1 started <0 album.publish workflow",
		"2 started <1 album.export workflow",
		`2 warned msg="2 raw files skipped"`,
		"2 succeeded",
		"1 succeeded",
		"3 started <0 album.extras workflow",
		`3 warned msg="a second root"`,
		"3 succeeded",
	}, events.lines())
}

func TestWithNode(t *testing.T) {
	tracker, events, _ := newTestTracker()
	ctx, root := tracker.Begin(context.Background(), stepPublish)

	pauseCtx, pause := progress.Run(ctx, stepPause)
	derived, cancel := context.WithCancel(pauseCtx)
	defer cancel()
	pause.End(nil)
	_, write := progress.Run(progress.WithNode(derived, root), stepWrite)
	write.End(nil)

	assert.Equal(t, []string{
		"1 started <0 album.publish workflow",
		"2 started <1 queue.pause activity",
		"2 succeeded",
		"3 started <1 manifest.write activity",
		"3 succeeded",
	}, events.lines())
}

func TestBatch_UnderAnErrgroup(t *testing.T) {
	// Arrange
	errQueue := errors.New("queue unreachable")
	tracker, events, _ := newTestTracker()
	ctx, root := progress.Begin(progress.WithTracker(context.Background(), tracker), stepPublish)
	batchCtx, batch := progress.Batch(ctx, stepResize, 2)

	// Act
	eg, egCtx := errgroup.WithContext(batchCtx)
	failed := make(chan struct{})
	eg.Go(func() error {
		_, dunes := progress.Run(egCtx, newStepPhoto("dunes.jpg"))
		dunes.End(errQueue)
		close(failed)
		return errQueue
	})
	eg.Go(func() error {
		_, beach := progress.Run(egCtx, newStepPhoto("beach.jpg"))
		<-failed
		<-egCtx.Done()
		err := fmt.Errorf("resize: %w", context.Cause(egCtx))
		beach.End(err)
		return err
	})
	err := eg.Wait()
	batch.End(err)
	root.End(err)

	// Assert
	lines := events.lines()
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, []string{"2 failed items=0/1/1/2", "1 failed"}, lines[len(lines)-2:])
	assert.Equal(t, 1, countContaining(lines, `error="queue unreachable"`), "the cause is in the stream once")
	assert.Equal(t, 1, countContaining(lines, " cancelled"), "the sibling the errgroup cancelled")
}

func countContaining(lines []string, substr string) int {
	var n int
	for _, line := range lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func TestBatch_ConcurrentChildren(t *testing.T) {
	const photos = 50
	tracker, events, _ := newTestTracker()
	ctx, batch := progress.Batch(withRoot(context.Background(), tracker), stepResize, photos)

	var wg sync.WaitGroup
	for i := range photos {
		wg.Go(func() {
			_, node := progress.Run(ctx, newStepPhoto(fmt.Sprint(i)), progress.MeasureBytes)
			node.Progress(progress.Sample{Bytes: &progress.Bytes{Done: int64(i)}})
			if i%5 == 0 {
				node.Fail("", errors.New("unable to resize"))
				return
			}
			node.End(nil)
		})
	}
	wg.Wait()
	batch.End(nil)

	require.GreaterOrEqual(t, len(events.events), 2)
	assert.Equal(t, "1 started <0 album.publish workflow", formatEvent(events.events[0]))
	assert.Equal(t, "2 started <1 photo.resize workflow [items] items=0/0/0/50", formatEvent(events.events[1]))
	last := events.events[len(events.events)-1]
	assert.Equal(t, "2 succeeded items=40/10/0/50", formatEvent(last))
	opened := make(map[progress.NodeID]bool)
	for _, evt := range events.events {
		if evt.Open != nil {
			opened[evt.Node] = true
			assert.True(t, opened[evt.Open.Parent] || evt.Open.Parent == 0, "node %d opened before its parent", evt.Node)
		}
		assert.True(t, opened[evt.Node], "node %d reported before it opened", evt.Node)
	}
}

func TestBegin_UnderAnEndedWorkflow(t *testing.T) {
	var logs bytes.Buffer
	tracker, events, _ := newTestTracker()
	ctx, root := tracker.Begin(newLoggedContext(&logs), stepPublish)
	root.End(nil)

	_, write := progress.Run(ctx, stepWrite)
	write.End(nil)

	assert.Empty(t, events.lines())
	assert.Contains(t, logs.String(), "progress: the parent has ended, the node is mute")
}
