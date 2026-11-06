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

func newFixedRenderer(out *syncBuffer) *Renderer {
	renderer := New(out)
	renderer.now = func() time.Time { return start }
	return renderer
}

// newFinalTree is the final tree Stop writes for events, as out shows it.
func newFinalTree(events []progress.Event) string {
	final, _ := render(newTreeOf(events), frame{now: start, final: true})
	return ansi.Strip(final) + "\n"
}

// TestRenderer_Render races reports and log lines against the program's reads; run it with -race.
func TestRenderer_Render(t *testing.T) {
	var (
		renderer = newFixedRenderer(&syncBuffer{})
		events   = newResourceStream(t)
		wg       sync.WaitGroup
		drained  int
		stop     = make(chan struct{})
		done     = make(chan struct{})
	)
	// The goroutine below reads the tree and the log in the program's place.
	renderer.state = stateRunning
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				render(renderer.snapshot(), frame{now: start})
				drained += len(renderer.drainLogs())
			}
		}
	}()

	for i := range 8 {
		wg.Go(func() {
			for _, evt := range events {
				evt.StepID += progress.StepID(i * 100)
				if evt.Open != nil && evt.Open.Parent != 0 {
					open := *evt.Open
					open.Parent += progress.StepID(i * 100)
					evt.Open = &open
				}
				renderer.Render(evt)
				_, err := fmt.Fprintf(renderer.LogWriter(), "line %d\n", i)
				assert.NoError(t, err)
			}
		})
	}
	wg.Wait()
	close(stop)
	<-done

	total := drained + len(renderer.drainLogs())
	assert.Equal(t, 8*len(events), total)
	assert.Len(t, renderer.snapshot().roots, 8)
}

// Before the first event there is no tree: the log goes straight to out, and Stop draws nothing.
func TestRenderer_Stop_beforeTheFirstEvent(t *testing.T) {
	var (
		out      = &syncBuffer{}
		renderer = newFixedRenderer(out)
	)
	_, err := renderer.LogWriter().Write([]byte("WRN slow disk\nERR lost\n"))
	require.NoError(t, err)

	drawn := renderer.Stop(StopOptions{})
	again := renderer.Stop(StopOptions{})
	_, err = renderer.LogWriter().Write([]byte("WRN late\n"))
	require.NoError(t, err)

	assert.False(t, drawn)
	assert.False(t, again)
	assert.Nil(t, renderer.program, "no program was started")
	assert.Equal(t, "WRN slow disk\nERR lost\nWRN late\n", ansi.Strip(out.String()))
}

func TestRenderer_LogWriter_afterStop(t *testing.T) {
	var (
		out      = &syncBuffer{}
		renderer = newFixedRenderer(out)
	)
	renderer.Stop(StopOptions{})

	_, err := renderer.LogWriter().Write([]byte("WRN late\n"))

	require.NoError(t, err)
	assert.Equal(t, "WRN late\n", ansi.Strip(out.String()))
}

// The first event starts the program on a buffer: the log is printed once, above the final tree,
// which comes last.
func TestRenderer_Render_startsTheProgram(t *testing.T) {
	var (
		out      = &syncBuffer{}
		renderer = newFixedRenderer(out)
		events   = newFinishedStream(t)
		final    = newFinalTree(events)
	)
	for _, evt := range events {
		renderer.Render(evt)
	}
	_, err := renderer.LogWriter().Write([]byte("WRN slow disk\n"))
	require.NoError(t, err)

	drawn := renderer.Stop(StopOptions{})
	again := renderer.Stop(StopOptions{})

	got := ansi.Strip(out.String())
	assert.True(t, drawn)
	assert.False(t, again, "the tree is written once")
	assert.True(t, strings.HasSuffix(got, final), "the final tree comes last:\n%s", got)
	assert.Equal(t, 1, strings.Count(got, "WRN slow disk"))
	assert.Less(t, strings.Index(got, "WRN slow disk"), strings.Index(got, final))
}

func TestRenderer_Render_afterStop(t *testing.T) {
	renderer := newFixedRenderer(&syncBuffer{})
	renderer.Stop(StopOptions{})

	renderer.Render(newFinishedStream(t)[0])

	assert.Nil(t, renderer.program, "a stopped renderer starts no program")
}

// TestRenderer_Stop_afterRunExits covers a program that ended on its own: the log and the tree go
// straight to out.
func TestRenderer_Stop_afterRunExits(t *testing.T) {
	var (
		out      = &syncBuffer{}
		renderer = newFixedRenderer(out)
		events   = newFinishedStream(t)
	)
	renderer.Render(events[0])
	renderer.program.Quit()
	<-renderer.done
	start := len(out.String())
	for _, evt := range events[1:] {
		renderer.Render(evt)
	}
	_, err := renderer.LogWriter().Write([]byte("WRN late\n"))
	require.NoError(t, err)

	renderer.Stop(StopOptions{})

	assert.Equal(t, "WRN late\n"+newFinalTree(events), ansi.Strip(out.String()[start:]))
}
