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
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

// syncBuffer is a bytes.Buffer safe for the program's goroutine and the test's.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newFinishedStream(t *testing.T) []progress.Event {
	t.Helper()
	return newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
		ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
		_, file := progress.Run(ctx, progress.Step{Kind: "file", ID: "beach.jpg"})
		clock.advance(ms(400))
		file.End(nil)
		root.End(nil)
	})
}

// newResourceStream updates one resource twice, so a reader of the tree sees a line rewritten.
func newResourceStream(t *testing.T) []progress.Event {
	t.Helper()
	return newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
		ctx, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
		_, pause := progress.Run(ctx, progress.Step{Kind: "queue.pause"}, progress.MeasureResources)
		worker := progress.Resource{Kind: "worker", ID: "worker-1"}
		pause.UpdateResource(worker, "Draining")
		root.Warn("slow")
		pause.UpdateResource(worker, "Paused")
		pause.End(nil)
		root.End(nil)
	})
}

func newFixedReporter(out *syncBuffer) *Reporter {
	reporter := New(out)
	reporter.now = func() time.Time { return start }
	return reporter
}

// newFinalTree is the final tree Stop writes for events, as out shows it.
func newFinalTree(events []progress.Event) string {
	final, _ := render(newTreeOf(events), frame{now: start, final: true})
	return ansi.Strip(final) + "\n"
}

// TestReporter_Report races reports and log lines against the program's reads; run it with -race.
func TestReporter_Report(t *testing.T) {
	var (
		reporter = newFixedReporter(&syncBuffer{})
		events   = newResourceStream(t)
		wg       sync.WaitGroup
		drained  int
		stop     = make(chan struct{})
		done     = make(chan struct{})
	)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				render(reporter.snapshot(), frame{now: start})
				drained += len(reporter.drainLogs())
			}
		}
	}()

	for i := range 8 {
		wg.Go(func() {
			for _, evt := range events {
				evt.Node += progress.NodeID(i * 100)
				if evt.Open != nil && evt.Open.Parent != 0 {
					open := *evt.Open
					open.Parent += progress.NodeID(i * 100)
					evt.Open = &open
				}
				reporter.Report(evt)
				_, err := fmt.Fprintf(reporter.LogWriter(), "line %d\n", i)
				assert.NoError(t, err)
			}
		})
	}
	wg.Wait()
	close(stop)
	<-done

	total := drained + len(reporter.drainLogs())
	assert.Equal(t, 8*len(events), total)
	assert.Len(t, reporter.snapshot().roots, 8)
}

func TestReporter_Stop_beforeStart(t *testing.T) {
	var (
		out      = &syncBuffer{}
		reporter = newFixedReporter(out)
	)
	for _, evt := range newFinishedStream(t) {
		reporter.Report(evt)
	}
	_, err := reporter.LogWriter().Write([]byte("WRN slow disk\nERR lost\n"))
	require.NoError(t, err)

	reporter.Stop(StopOptions{})
	reporter.Stop(StopOptions{})

	assert.Equal(t, "WRN slow disk\nERR lost\n"+
		"✔ album.publish summer  0.4s\n"+
		"└── ✔ file beach.jpg    0.4s\n",
		ansi.Strip(out.String()))
}

func TestReporter_LogWriter_afterStop(t *testing.T) {
	var (
		out      = &syncBuffer{}
		reporter = newFixedReporter(out)
	)
	reporter.Stop(StopOptions{})

	_, err := reporter.LogWriter().Write([]byte("WRN late\n"))

	require.NoError(t, err)
	assert.Equal(t, "WRN late\n", ansi.Strip(out.String()))
}

// TestReporter_Start runs the program on a buffer: the log is printed once, above the final tree,
// which comes last.
func TestReporter_Start(t *testing.T) {
	var (
		out      = &syncBuffer{}
		reporter = newFixedReporter(out)
		events   = newFinishedStream(t)
		final    = newFinalTree(events)
	)
	require.NoError(t, reporter.Start())
	for _, evt := range events {
		reporter.Report(evt)
	}
	_, err := reporter.LogWriter().Write([]byte("WRN slow disk\n"))
	require.NoError(t, err)

	reporter.Stop(StopOptions{})
	reporter.Stop(StopOptions{})

	got := ansi.Strip(out.String())
	assert.True(t, strings.HasSuffix(got, final), "the final tree comes last:\n%s", got)
	assert.Equal(t, 1, strings.Count(got, "WRN slow disk"))
	assert.Less(t, strings.Index(got, "WRN slow disk"), strings.Index(got, final))
}

func TestReporter_Start_afterStop(t *testing.T) {
	reporter := newFixedReporter(&syncBuffer{})
	reporter.Stop(StopOptions{})

	err := reporter.Start()

	assert.ErrorIs(t, err, errStarted)
}

// TestReporter_Stop_afterRunExits covers a program that ended on its own: the log and the tree go
// straight to out.
func TestReporter_Stop_afterRunExits(t *testing.T) {
	var (
		out      = &syncBuffer{}
		reporter = newFixedReporter(out)
		events   = newFinishedStream(t)
	)
	require.NoError(t, reporter.Start())
	reporter.program.Quit()
	<-reporter.done
	start := len(out.String())
	for _, evt := range events {
		reporter.Report(evt)
	}
	_, err := reporter.LogWriter().Write([]byte("WRN late\n"))
	require.NoError(t, err)

	reporter.Stop(StopOptions{})

	assert.Equal(t, "WRN late\n"+newFinalTree(events), ansi.Strip(out.String()[start:]))
}
