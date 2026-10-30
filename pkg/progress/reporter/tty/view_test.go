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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

var errDenied = errors.New("403 forbidden")

func TestRender(t *testing.T) {
	tests := []struct {
		name     string
		workflow func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock)
		frame    frame
		// after is how long after the last event the frame is drawn.
		after time.Duration
	}{
		{name: "publish_live", workflow: publishHalfway, frame: frame{width: 80, height: 24}},
		{name: "backup_final", workflow: backupFailed, frame: frame{width: 80, height: 24, final: true}},
		{name: "backup_live", workflow: backupFailed, frame: frame{width: 80, height: 24}},
		{name: "parallel_peak", workflow: parallelPeak, frame: frame{width: 80, height: 24}},
		{name: "overflow", workflow: manyFinished, frame: frame{width: 80, height: 8}},
		{name: "publish_low", workflow: publishHalfway, frame: frame{width: 80, height: 6}},
		{name: "backup_final_narrow", workflow: backupFailed, frame: frame{width: 40, height: 6, final: true}},
		{name: "narrow", workflow: parallelPeak, frame: frame{width: 40, height: 24}},
		{name: "medium", workflow: parallelPeak, frame: frame{width: 60, height: 24}},
		{name: "unknown_total", workflow: unknownTotal, frame: frame{width: 80, height: 24}},
		{name: "exceeded_total", workflow: exceededTotal, frame: frame{width: 80, height: 24}},
		{name: "two_roots", workflow: twoRoots, frame: frame{width: 80, height: 24}},
		{name: "wide", workflow: publishHalfway, frame: frame{width: 200, height: 24}},
		{name: "column_sticky", workflow: twoRoots, frame: frame{width: 80, height: 24, minColumn: 60}},
		{name: "column_capped", workflow: twoRoots, frame: frame{width: 50, height: 24, minColumn: 70}},
		{name: "quiet_root_final", workflow: publishQuietly, frame: frame{width: 80, height: 24, final: true}},
		{name: "cancelled_in_flight", workflow: cancelInFlight, frame: frame{width: 80, height: 24, final: true}},
		{name: "cancelled_kinds", workflow: cancelKinds, frame: frame{width: 80, height: 24}},
		{name: "final_ignores_column", workflow: twoRoots, frame: frame{width: 80, height: 24, minColumn: 60, final: true}},
		{name: "stop_just_ended", workflow: stopThenStart, frame: frame{width: 80, height: 24}},
		{name: "stop_folded", workflow: stopThenStart, frame: frame{width: 80, height: 24}, after: ms(700)},
		{name: "essential_failed", workflow: backupFailed, frame: frame{width: 80, height: 24, final: true, StopOptions: StopOptions{Essential: true, OmitRootError: true}}},
		{name: "final_root_error_omitted", workflow: refusedAtRoot, frame: frame{width: 80, height: 24, final: true, StopOptions: StopOptions{OmitRootError: true}}},
		{name: "final_root_error", workflow: refusedAtRoot, frame: frame{width: 80, height: 24, final: true}},
		{name: "essential_quiet", workflow: publishQuietly, frame: frame{width: 80, height: 24, final: true, StopOptions: StopOptions{Essential: true}}},
		{name: "essential_in_result", workflow: publishWarned, frame: frame{width: 80, height: 24, final: true, StopOptions: StopOptions{Essential: true}}},
		{name: "essential_below_stages", workflow: exportMixed, frame: frame{width: 80, height: 24, final: true, StopOptions: StopOptions{Essential: true}}},
		{name: "final_in_result", workflow: publishWarned, frame: frame{width: 80, height: 24, final: true}},
		{name: "final_many_warnings", workflow: manyWarnings, frame: frame{width: 80, height: 24, final: true, StopOptions: StopOptions{MoreHint: "see the log"}}},
		{name: "live_many_warnings", workflow: manyWarnings, frame: frame{width: 80, height: 24}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				events = newStream(t, tt.workflow)
				f      = tt.frame
			)
			require.NotEmpty(t, events)
			f.now = events[len(events)-1].At.Add(tt.after)

			got, _ := render(newTreeOf(events), f)

			assertGolden(t, tt.name+".golden", ansi.Strip(got)+"\n")
		})
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: ms(400), want: "0.4s"},
		{elapsed: ms(12_340), want: "12.3s"},
		{elapsed: 62 * time.Second, want: "1m02s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatElapsed(tt.elapsed))
		})
	}
}

// publishHalfway pauses the queue, exports with a warning and leaves an upload batch running.
func publishHalfway(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"}, progress.MeasureResources)
	for _, worker := range []string{"worker-1", "worker-2"} {
		pause.UpdateResource(progress.Resource{Kind: "worker", ID: worker}, "Paused")
		clock.advance(ms(200))
	}
	pause.Done(progress.Sample{})

	exportCtx, export := progress.Begin(ctx, progress.Step{Kind: "album.export"})
	export.Warn("2 raw files skipped")
	resizeCtx, resize := progress.Batch(exportCtx, progress.Step{Kind: "photo.resize"}, 3)
	for _, name := range []string{"beach.jpg", "dunes.jpg", "sunset.jpg"} {
		_, photo := progress.Run(resizeCtx, progress.Step{Kind: "photo", ID: name}, progress.MeasureBytes)
		clock.advance(ms(300))
		photo.Done(progress.Sample{Bytes: &progress.Bytes{Done: 2_000_000, Total: 2_000_000, Stored: 400_000}})
	}
	resize.End(nil)
	export.End(nil)

	uploadCtx, _ := progress.Batch(ctx, progress.Step{Kind: "cdn.upload"}, 3)
	_, beach := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "beach.jpg"}, progress.MeasureBytes)
	clock.advance(ms(400))
	beach.Done(progress.Sample{Bytes: &progress.Bytes{Done: 2_000_000, Total: 2_000_000}})
	_, dunes := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "dunes.jpg"}, progress.MeasureBytes)
	clock.advance(ms(500))
	dunes.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 1_000_000, Total: 2_000_000}})
}

// backupFailed fails one upload, cancels the rest and rolls back with a warning.
func backupFailed(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.backup", Name: "summer"})
	uploadCtx, upload := progress.Batch(ctx, progress.Step{Kind: "cdn.upload"}, 4)
	_, beach := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "beach.jpg"}, progress.MeasureBytes)
	clock.advance(ms(350))
	beach.Done(progress.Sample{Bytes: &progress.Bytes{Done: 2_100_000, Total: 2_100_000}})
	_, raw := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "raw.dng"}, progress.MeasureBytes)
	clock.advance(ms(250))
	raw.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 450_000}})
	raw.Fail("unable to upload", errDenied)
	for _, name := range []string{"cover.jpg", "map.png"} {
		_, file := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: name}, progress.MeasureBytes)
		file.End(context.Canceled)
	}
	upload.End(errDenied)

	rollbackCtx, rollback := progress.Begin(ctx, progress.Step{Kind: "album.rollback"})
	purgeCtx, purge := progress.Batch(rollbackCtx, progress.Step{Kind: "cdn.purge"}, 1)
	_, file := progress.Run(purgeCtx, progress.Step{Kind: "file", ID: "beach.jpg"})
	clock.advance(ms(50))
	file.Warn("purge failed, the file stays on the CDN: edge timeout")
	file.Done(progress.Sample{})
	purge.End(nil)
	rollback.End(nil)
	root.End(errDenied)
}

// parallelPeak runs four uploads at once at different points, two of eight already done.
func parallelPeak(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	uploadCtx, _ := progress.Batch(ctx, progress.Step{Kind: "cdn.upload"}, 8)
	for i := range 2 {
		_, file := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: fmt.Sprintf("photo-%02d.jpg", i)}, progress.MeasureBytes)
		file.Done(progress.Sample{Bytes: &progress.Bytes{Done: 1_000_000, Total: 1_000_000}})
	}
	var files []*progress.Node
	for i := 2; i < 6; i++ {
		_, file := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: fmt.Sprintf("photo-%02d.jpg", i)}, progress.MeasureBytes)
		files = append(files, file)
		clock.advance(ms(250))
	}
	for i, file := range files {
		file.Progress(progress.Sample{Bytes: &progress.Bytes{Done: int64(i+1) * 1_000_000, Total: 5_000_000}})
	}
}

// manyFinished is a batch of twelve, ten done, more than eight lines can show.
func manyFinished(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "album.backup", Name: "summer"})
	_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"})
	pause.Done(progress.Sample{})
	exportCtx, export := progress.Begin(ctx, progress.Step{Kind: "album.export"})
	for i := range 6 {
		_, photo := progress.Run(exportCtx, progress.Step{Kind: "photo", ID: fmt.Sprintf("photo-%02d.jpg", i)})
		clock.advance(ms(100))
		photo.Warn("recompressed")
		photo.Done(progress.Sample{})
	}
	export.End(nil)
	uploadCtx, _ := progress.Batch(ctx, progress.Step{Kind: "cdn.upload"}, 3)
	_, failed := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "raw.dng"})
	failed.Fail("unable to upload", errDenied)
	_, running := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "cover.jpg"})
	clock.advance(ms(100))
	running.Update("uploading")
}

// unknownTotal reads a source that has not named its total yet.
func unknownTotal(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "album.import"})
	_, file := progress.Run(ctx, progress.Step{Kind: "file", ID: "archive.zip"}, progress.MeasureBytes)
	clock.advance(2 * time.Second)
	file.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 3_000_000}})
}

// exceededTotal reads past the total it was told.
func exceededTotal(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "album.import"})
	_, file := progress.Run(ctx, progress.Step{Kind: "file", ID: "archive.zip"}, progress.MeasureBytes)
	clock.advance(time.Second)
	file.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 2_500_000, Total: 2_000_000}})
}

// twoRoots begins twice on one tracker.
func twoRoots(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	firstCtx, first := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	_, export := progress.Run(firstCtx, progress.Step{Kind: "album.export"})
	clock.advance(ms(700))
	export.Done(progress.Sample{})
	first.End(nil)
	secondCtx, _ := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "winter"})
	_, pause := progress.Run(secondCtx, progress.Step{Kind: "queue.pause"})
	clock.advance(ms(300))
	pause.Update("Draining")
}

// publishQuietly succeeds with nothing to say.
func publishQuietly(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"}, progress.MeasureResources)
	pause.UpdateResource(progress.Resource{Kind: "worker", ID: "worker-1"}, "Paused")
	clock.advance(ms(200))
	pause.Done(progress.Sample{})
	exportCtx, export := progress.Begin(ctx, progress.Step{Kind: "album.export"})
	resizeCtx, resize := progress.Batch(exportCtx, progress.Step{Kind: "photo.resize"}, 2)
	for _, name := range []string{"beach.jpg", "dunes.jpg"} {
		_, photo := progress.Run(resizeCtx, progress.Step{Kind: "photo", ID: name}, progress.MeasureBytes)
		clock.advance(ms(300))
		photo.Done(progress.Sample{Bytes: &progress.Bytes{Done: 2_000_000, Total: 2_000_000}})
	}
	resize.End(nil)
	export.End(nil)
	root.End(nil)
}

// stopThenStart stops two workers quietly, then starts them, 0.8s in.
func stopThenStart(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "queue.restart"})
	_, stop := progress.Run(ctx, progress.Step{Kind: "queue.stop"}, progress.MeasureResources)
	for _, worker := range []string{"worker-1", "worker-2"} {
		stop.SettleResource(progress.Resource{Kind: "worker", ID: worker}, "Stopped")
		clock.advance(ms(200))
	}
	stop.Done(progress.Sample{})
	_, start := progress.Run(ctx, progress.Step{Kind: "queue.start"})
	clock.advance(ms(800))
	start.Update("Starting")
}

// refusedAtRoot fails on its root, past one quiet step.
func refusedAtRoot(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"})
	clock.advance(ms(200))
	pause.Done(progress.Sample{})
	root.End(errDenied)
}

// publishWarned warns of what its result repeats, of a resource and of what it alone says.
func publishWarned(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	root.WarnInResult("2 raw files skipped")
	_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"}, progress.MeasureResources)
	pause.SettleResource(progress.Resource{Kind: "worker", ID: "worker-1"}, "Paused")
	pause.WarnResource(progress.Resource{Kind: "worker", ID: "worker-2"}, "unreachable")
	clock.advance(ms(200))
	pause.Done(progress.Sample{})
	_, export := progress.Run(ctx, progress.Step{Kind: "album.export"})
	clock.advance(ms(300))
	export.WarnInResult("cover.jpg recompressed")
	export.Done(progress.Sample{})
	_, upload := progress.Run(ctx, progress.Step{Kind: "cdn.upload"})
	clock.advance(ms(100))
	upload.Warn("edge timeout, retried")
	upload.Done(progress.Sample{})
	root.End(nil)
}

// exportMixed exports two photos, one quietly, one with a warning.
func exportMixed(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	exportCtx, export := progress.Begin(ctx, progress.Step{Kind: "album.export"})
	for _, name := range []string{"beach.jpg", "dunes.jpg"} {
		_, photo := progress.Run(exportCtx, progress.Step{Kind: "photo", ID: name})
		clock.advance(ms(100))
		if name == "dunes.jpg" {
			photo.Warn("recompressed")
		}
		photo.Done(progress.Sample{})
	}
	export.End(nil)
	root.End(nil)
}

// manyWarnings warns seven times of one node.
func manyWarnings(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	_, export := progress.Run(ctx, progress.Step{Kind: "album.export"})
	for i := range 7 {
		clock.advance(ms(100))
		export.Warn(fmt.Sprintf("photo-%02d.jpg recompressed", i))
	}
	export.Done(progress.Sample{})
	root.End(nil)
}

// cancelInFlight cancels an upload batch with one file halfway through and one not begun.
func cancelInFlight(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	uploadCtx, upload := progress.Batch(ctx, progress.Step{Kind: "cdn.upload"}, 3)
	_, beach := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "beach.jpg"}, progress.MeasureBytes)
	clock.advance(ms(300))
	beach.Done(progress.Sample{Bytes: &progress.Bytes{Done: 2_000_000, Total: 2_000_000}})
	_, dunes := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "dunes.jpg"}, progress.MeasureBytes)
	clock.advance(ms(400))
	dunes.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 1_000_000, Total: 2_000_000}})
	dunes.End(context.Canceled)
	_, sunset := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "sunset.jpg"}, progress.MeasureBytes)
	sunset.End(context.Canceled)
	upload.End(context.Canceled)
	root.End(context.Canceled)
}

// cancelKinds cancels a node outside a batch before it ran, beside one finished whole, and under
// a batch each way a cancellation has something to say, beside one that has not.
func cancelKinds(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
	ctx, _ = tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
	_, logo := progress.Run(ctx, progress.Step{Kind: "file", ID: "logo.png"}, progress.MeasureBytes)
	clock.advance(ms(200))
	logo.Done(progress.Sample{Bytes: &progress.Bytes{Done: 2_000_000, Total: 2_000_000}})
	_, cover := progress.Run(ctx, progress.Step{Kind: "file", ID: "cover.jpg"}, progress.MeasureBytes)
	cover.End(context.Canceled)

	uploadCtx, _ := progress.Batch(ctx, progress.Step{Kind: "cdn.upload"}, 5)
	_, slow := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "slow.jpg"}, progress.MeasureBytes)
	clock.advance(ms(300))
	slow.End(context.Canceled)
	_, fast := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "fast.jpg"}, progress.MeasureBytes)
	fast.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 500_000, Total: 2_000_000}})
	fast.End(context.Canceled)
	_, warned := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "warned.jpg"}, progress.MeasureBytes)
	warned.Warn("checksum skipped")
	warned.End(context.Canceled)
	_, quiet := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "quiet.jpg"}, progress.MeasureBytes)
	quiet.End(context.Canceled)
	_, running := progress.Run(uploadCtx, progress.Step{Kind: "file", ID: "map.png"}, progress.MeasureBytes)
	clock.advance(ms(100))
	running.Progress(progress.Sample{Bytes: &progress.Bytes{Done: 100_000, Total: 400_000}})
}

// assertGolden compares got with testdata/name, rewriting it under -update.
func assertGolden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
}
