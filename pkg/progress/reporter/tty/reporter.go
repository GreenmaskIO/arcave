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

// Package tty renders a progress stream as a live tree on a terminal, through Bubble Tea.
package tty

import (
	"io"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/greenmaskio/arcave/pkg/progress"
)

type reporterState int

const (
	// stateIdle is before the first event: log lines go straight to out.
	stateIdle reporterState = iota
	stateRunning
	// stateAbandoned is a program that ended on its own: what is written goes straight to out.
	stateAbandoned
	stateStopping
	stateStopped
)

// StopOptions say how Stop writes the final tree.
type StopOptions struct {
	// Essential writes the roots, each child of a root as its line and time, and below them only
	// the paths to what a result printed beside the tree does not say: failures, cancellations and
	// warnings not marked InResult.
	Essential bool
	// OmitRootError leaves a root's error line out, for a caller that writes the error itself.
	OmitRootError bool
	// MoreHint follows the count of the warnings the final tree leaves out of a node.
	MoreHint string
}

// Reporter draws the stream as a tree redrawn in place, with log lines printed above it.
// While the program runs, Report and the log writer never wait on the terminal: it reads them on
// its own tick.
type Reporter struct {
	out io.Writer
	now func() time.Time

	mu      sync.Mutex
	state   reporterState
	tree    *tree
	logs    []string
	width   int
	program *tea.Program
	done    chan struct{}
}

// New returns a Reporter drawing to out, a terminal. It takes the terminal on the first event, so
// a run that reports none never does; Stop must follow on every path, a panic included, or the
// terminal is left without its cursor.
func New(out io.Writer) *Reporter {
	return &Reporter{
		out:  out,
		now:  time.Now,
		tree: newTree(),
	}
}

// Report reads evt into the tree, starting the program drawing it on the first event; it neither
// writes nor waits.
func (r *Reporter) Report(evt progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tree.apply(evt)
	if r.state == stateIdle {
		r.startLocked()
	}
}

// LogWriter returns the writer log lines go through: printed above the tree, or straight to out
// while there is none. It filters nothing; the logger's level does.
func (r *Reporter) LogWriter() io.Writer {
	return logWriter{reporter: r}
}

// Stop clears the live frame, gives the terminal back and writes what is left of the log and the
// final tree to out, as opts say; it reports whether it wrote a tree, which a run that reported
// no event has not. It is idempotent.
func (r *Reporter) Stop(opts StopOptions) bool {
	r.mu.Lock()
	switch r.state {
	case stateIdle:
		r.state = stateStopped
		r.mu.Unlock()
		return false
	case stateStopped, stateStopping:
		r.mu.Unlock()
		return false
	case stateRunning:
		r.state = stateStopping
		program, done := r.program, r.done
		r.mu.Unlock()
		program.Send(stopMsg{})
		<-done
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	r.state = stateStopped
	r.writeLocked(r.drainLocked()...)
	final, _ := render(r.tree, frame{
		now:         r.now(),
		width:       r.width,
		final:       true,
		StopOptions: opts,
	})
	r.writeLocked(final)
	return true
}

func (r *Reporter) startLocked() {
	program := tea.NewProgram(
		newModel(r, r.now),
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
		tea.WithOutput(r.out),
	)
	r.program = program
	r.state = stateRunning
	r.done = make(chan struct{})
	go r.run(program, r.done)
}

func (r *Reporter) run(program *tea.Program, done chan struct{}) {
	//nolint:errcheck // a program that fails is abandoned: the reporter writes to out itself
	_, _ = program.Run()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == stateRunning {
		r.state = stateAbandoned
		r.writeLocked(r.drainLocked()...)
	}
	close(done)
}

// snapshot copies the tree for the program to render.
func (r *Reporter) snapshot() *tree {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tree.snapshot()
}

// drainLogs takes the log lines written since the last call.
func (r *Reporter) drainLogs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drainLocked()
}

// resize keeps the terminal's width for the final tree.
func (r *Reporter) resize(width int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.width = width
}

func (r *Reporter) drainLocked() []string {
	logs := r.logs
	r.logs = nil
	return logs
}

// writeLocked writes lines straight to out, colors downsampled to what it takes.
func (r *Reporter) writeLocked(lines ...string) {
	for _, line := range lines {
		if line == "" {
			continue
		}
		//nolint:errcheck // a terminal that fails a write has nowhere to report it
		_, _ = lipgloss.Fprintln(r.out, line)
	}
}

type logWriter struct {
	reporter *Reporter
}

// Write takes p as whole lines: buffered for the program's next tick while it runs, else
// written to out.
func (w logWriter) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimSuffix(string(p), "\n"), "\n")
	r := w.reporter
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != stateRunning && r.state != stateStopping {
		r.writeLocked(lines...)
		return len(p), nil
	}
	r.logs = append(r.logs, lines...)
	return len(p), nil
}
