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
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/pkg/progress"
)

type fakeSource struct {
	tree  *tree
	logs  []string
	width int
}

func (s *fakeSource) snapshot() *tree {
	return s.tree.snapshot()
}

func (s *fakeSource) resize(width int) {
	s.width = width
}

func (s *fakeSource) drainLogs() []string {
	logs := s.logs
	s.logs = nil
	return logs
}

func TestModel_Update(t *testing.T) {
	finished := newTreeOf(newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
		_, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish", Name: "summer"})
		root.Warn("slow")
		clock.advance(ms(400))
		root.End(nil)
	}))
	tests := []struct {
		name        string
		stopped     bool
		column      int
		logs        []string
		msg         tea.Msg
		want        string
		wantSpin    int
		wantStopped bool
		wantWidth   int
		wantHeight  int
		wantColumn  int
		wantView    string
		// wantLogs is what is left in the source.
		wantLogs        []string
		wantSourceWidth int
	}{
		{
			name:       "a tick renders the frame, prints the log lines at once, then ticks again",
			logs:       []string{"WRN slow disk", "ERR lost"},
			msg:        tickMsg{},
			want:       "sequence[println: WRN slow disk\nERR lost, tick]",
			wantSpin:   1,
			wantWidth:  30,
			wantColumn: 28,
			wantView:   "✔ album.publish summer  0.4s\n  ! slow",
		},
		{
			name:       "a tick with no log only ticks again",
			msg:        tickMsg{},
			want:       "tick",
			wantSpin:   1,
			wantWidth:  30,
			wantColumn: 28,
			wantView:   "✔ album.publish summer  0.4s\n  ! slow",
		},
		{
			name:       "a tick keeps a wider column",
			column:     30,
			msg:        tickMsg{},
			want:       "tick",
			wantSpin:   1,
			wantWidth:  30,
			wantColumn: 30,
			wantView:   "✔ album.publish summer    0.4s\n  ! slow",
		},
		{
			name:        "stop only marks the model, leaving the log to the reporter",
			logs:        []string{"WRN slow disk"},
			msg:         stopMsg{},
			wantLogs:    []string{"WRN slow disk"},
			wantStopped: true,
			wantWidth:   30,
		},
		{
			name:        "a tick after stop quits",
			stopped:     true,
			logs:        []string{"WRN late"},
			msg:         tickMsg{},
			want:        "quit",
			wantLogs:    []string{"WRN late"},
			wantStopped: true,
			wantWidth:   30,
		},
		{
			name:            "a resize sets the frame's size, renders it and tells the source",
			msg:             tea.WindowSizeMsg{Width: 60, Height: 15},
			wantWidth:       60,
			wantHeight:      15,
			wantColumn:      28,
			wantView:        "✔ album.publish summer  0.4s\n  ! slow",
			wantSourceWidth: 60,
		},
		{
			name:            "a narrowing resize caps the frame's column but keeps the wider one",
			column:          40,
			msg:             tea.WindowSizeMsg{Width: 26, Height: 15},
			wantWidth:       26,
			wantHeight:      15,
			wantColumn:      40,
			wantView:        "✔ album.publish sum…  0.4s\n  ! slow",
			wantSourceWidth: 26,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSource{tree: finished, logs: tt.logs}
			m := newModel(source, func() time.Time { return start })
			m.tick = func() tea.Cmd { return func() tea.Msg { return tickMsg{} } }
			m.width = 30
			m.stopped = tt.stopped
			m.column = tt.column

			updated, cmd := m.Update(tt.msg)

			got := updated.(model)
			assert.Equal(t, tt.want, runCmd(t, cmd))
			assert.Equal(t, tt.wantSpin, got.spin)
			assert.Equal(t, tt.wantStopped, got.stopped)
			assert.Equal(t, tt.wantWidth, got.width)
			assert.Equal(t, tt.wantHeight, got.height)
			assert.Equal(t, tt.wantColumn, got.column)
			assert.Equal(t, tt.wantView, ansi.Strip(got.view))
			assert.Equal(t, tt.wantLogs, source.logs)
			assert.Equal(t, tt.wantSourceWidth, source.width)
		})
	}
}

func TestModel_View(t *testing.T) {
	running := newTreeOf(newStream(t, func(ctx context.Context, tracker *progress.Tracker, clock *fakeClock) {
		_, root := tracker.Begin(ctx, progress.Step{Kind: "album.publish"})
		root.Warn("slow")
	}))
	tests := []struct {
		name    string
		stopped bool
		want    string
	}{
		{name: "live", want: "⠙ album.publish  0.0s\n  ! slow"},
		{name: "stopped", stopped: true, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(&fakeSource{tree: running}, func() time.Time { return start })
			m.tick = func() tea.Cmd { return nil }
			m.width = 30
			ticked, _ := m.Update(tickMsg{})
			m = ticked.(model)
			m.stopped = tt.stopped

			view := m.View()

			assert.Equal(t, tt.want, ansi.Strip(view.Content))
		})
	}
}

// runCmd runs cmd and what it batches or sequences, naming each message it yields: a group as
// batch[…] (concurrent) or sequence[…] (in order).
func runCmd(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		return ""
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return "batch[" + runCmds(t, batch) + "]"
	}
	value := reflect.ValueOf(msg)
	switch {
	case value.Kind() == reflect.Slice && value.Type().Elem() == reflect.TypeFor[tea.Cmd]():
		return "sequence[" + runCmds(t, value.Convert(reflect.TypeFor[[]tea.Cmd]()).Interface().([]tea.Cmd)) + "]"
	case value.Type().Name() == "printLineMessage":
		// bubbletea exports no accessor for the line a Println prints.
		return "println: " + ansi.Strip(value.FieldByName("messageBody").String())
	}
	switch msg.(type) {
	case tickMsg:
		return "tick"
	case tea.QuitMsg:
		return "quit"
	}
	require.Failf(t, "unexpected message", "%T", msg)
	return ""
}

func runCmds(t *testing.T, cmds []tea.Cmd) string {
	t.Helper()
	names := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		names = append(names, runCmd(t, cmd))
	}
	return strings.Join(names, ", ")
}
