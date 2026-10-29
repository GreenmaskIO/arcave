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
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// tickInterval is how often the frame is redrawn and the log drained.
const tickInterval = 100 * time.Millisecond

// source is the reporter's state as the program reads it.
type source interface {
	snapshot() *tree
	drainLogs() []string
	// resize tells the source the terminal's width, for the final tree it writes itself.
	resize(width int)
}

type tickMsg time.Time

// stopMsg asks the program to quit on its next tick, once the last print is in. The reporter
// writes the rest of the log and the final tree after the frame is gone, so a frame taller than
// the screen does not scroll into the scrollback.
type stopMsg struct{}

type model struct {
	source  source
	now     func() time.Time
	tick    func() tea.Cmd
	width   int
	height  int
	spin    int
	stopped bool
	// column is the widest the frame's time column has been: it never moves left.
	column int
	// view is the frame Update rendered last, which View shows.
	view string
}

func newModel(source source, now func() time.Time) model {
	return model{
		source: source,
		now:    now,
		tick:   newTick,
	}
}

func (m model) Init() tea.Cmd {
	return m.tick()
}

// Update redraws on a tick and a resize, printing the log lines written since above the frame
// before the next tick, so no two prints are ever in flight; after a stop, the next tick quits.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.source.resize(msg.Width)
		return m.withView(), nil
	case tickMsg:
		if m.stopped {
			return m, tea.Quit
		}
		m.spin++
		return m.withView(), tea.Sequence(newPrintln(m.source.drainLogs()), m.tick())
	case stopMsg:
		m.stopped = true
		return m, nil
	}
	return m, nil
}

// View is the live frame; empty once stopped, so quitting clears it.
func (m model) View() tea.View {
	if m.stopped {
		return tea.NewView("")
	}
	return tea.NewView(m.view)
}

// withView renders the frame, its time column no further left than any before it.
func (m model) withView() model {
	view, column := render(m.source.snapshot(), frame{
		now:       m.now(),
		width:     m.width,
		height:    m.height,
		spin:      m.spin,
		minColumn: m.column,
	})
	m.view, m.column = view, max(m.column, column)
	return m
}

func newTick() tea.Cmd {
	return tea.Tick(tickInterval, func(at time.Time) tea.Msg { return tickMsg(at) })
}

// newPrintln prints lines above the frame in one go: one render for the lot.
func newPrintln(lines []string) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	return tea.Println(strings.Join(lines, "\n"))
}
