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

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/arcave/pkg/progress/renderer/tty"
)

func TestResolveProgressMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		terminal bool
		format   string
		want     string
		wantErr  bool
	}{
		{
			name:     "auto on a terminal with text logs draws the tree",
			mode:     progressModeAuto,
			terminal: true,
			format:   utils.LogFormatText,
			want:     progressModeTTY,
		},
		{
			name:     "unset is auto",
			terminal: true,
			format:   utils.LogFormatText,
			want:     progressModeTTY,
		},
		{
			name:   "auto off a terminal logs",
			mode:   progressModeAuto,
			format: utils.LogFormatText,
			want:   progressModeLog,
		},
		{
			name:     "auto with json logs logs",
			mode:     progressModeAuto,
			terminal: true,
			format:   utils.LogFormatJson,
			want:     progressModeLog,
		},
		{
			name:   "an explicit mode stands off a terminal",
			mode:   progressModeTTY,
			format: utils.LogFormatJson,
			want:   progressModeTTY,
		},
		{
			name: "json",
			mode: progressModeJSON,
			want: progressModeJSON,
		},
		{
			name:    "an unknown mode is refused",
			mode:    "none",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveProgressMode(tt.mode, tt.terminal, tt.format)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		name           string
		format         string
		progressMode   string
		stdoutTerminal bool
		want           string
		wantErr        bool
	}{
		{
			name:           "auto under the tree on a terminal stdout is text",
			progressMode:   progressModeTTY,
			stdoutTerminal: true,
			want:           formatText,
		},
		{
			name:         "auto under the tree with stdout captured is json",
			progressMode: progressModeTTY,
			want:         formatJSON,
		},
		{
			name:           "auto under the log is json",
			format:         formatAuto,
			progressMode:   progressModeLog,
			stdoutTerminal: true,
			want:           formatJSON,
		},
		{
			name:         "an explicit text stands off a terminal",
			format:       formatText,
			progressMode: progressModeLog,
			want:         formatText,
		},
		{
			name:           "an explicit json stands under the tree",
			format:         formatJSON,
			progressMode:   progressModeTTY,
			stdoutTerminal: true,
			want:           formatJSON,
		},
		{
			name:         "text cannot end the json stream",
			format:       formatText,
			progressMode: progressModeJSON,
			wantErr:      true,
		},
		{
			name:    "an unknown format",
			format:  "yaml",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveFormat(tt.format, tt.progressMode, tt.stdoutTerminal)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		level string
		want  string
	}{
		{name: "none set under the tree is warn", mode: progressModeTTY, want: "warn"},
		{name: "none set in a log is info", mode: progressModeLog, want: "info"},
		{name: "none set in the json stream is info", mode: progressModeJSON, want: "info"},
		{name: "a level set stands under the tree", mode: progressModeTTY, level: "info", want: "info"},
		{name: "a level set stands in a log", mode: progressModeLog, level: "debug", want: "debug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := resolveLogLevel(tt.mode, tt.level)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestJSONOutput_Close(t *testing.T) {
	errPartial := errors.New("gc partial")
	tests := []struct {
		name   string
		result any
		err    error
		want   string
	}{
		{
			name:   "a result is the last line",
			result: map[string]int{"removed": 2},
			want:   `{"type":"result","result":{"removed":2}}` + "\n",
		},
		{
			name: "an error without a result",
			err:  errPartial,
			want: `{"type":"error","error":"gc partial"}` + "\n",
		},
		{
			name:   "a partial result carries its error on the same line",
			result: map[string]int{"removed": 1},
			err:    errPartial,
			want:   `{"type":"result","result":{"removed":1},"error":"gc partial"}` + "\n",
		},
		{
			name: "nothing to say writes nothing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			out := newJSONOutput(&buf)
			if tt.result != nil {
				require.NoError(t, out.Print(tt.result))
			}

			out.Close(tt.err)

			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestJSONOutput_Render(t *testing.T) {
	var buf bytes.Buffer
	out := newJSONOutput(&buf)
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	out.Render(progress.Event{
		StepID: 1,
		Status: progress.StatusStarted,
		At:     at,
	})
	require.NoError(t, out.Print("done"))
	out.Close(nil)

	assert.Equal(t,
		`{"type":"progress","event":{"node":1,"status":"started","sample":{"elapsed_ns":0},"at":"2026-10-29T12:00:00Z"}}`+"\n"+
			`{"type":"result","result":"done"}`+"\n",
		buf.String())
}

func TestTTYOutput_Write_BeforeTheFirstEvent(t *testing.T) {
	var stderr bytes.Buffer
	out := &ttyOutput{
		renderer: tty.New(&stderr),
		stderr:   &stderr,
	}

	_, err := out.Write([]byte("WRN a warning\n"))
	require.NoError(t, err)
	out.Close(errors.New("refused"))

	assert.Equal(t, "WRN a warning\nError: refused\n", stderr.String(), "no tree was started, none is drawn")
}

// The result waits for Close, which knows whether the run failed: a partial gc prints and fails.
func TestTTYOutput_Close_WritesTheResultThenTheError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	out := &ttyOutput{
		renderer: tty.New(&stderr),
		result:   resultOptions{format: formatText},
		stdout:   &stdout,
		stderr:   &stderr,
	}

	require.NoError(t, out.Print(model.RemoveResponse{}))
	assert.Empty(t, stdout.String(), "nothing is written before Close")
	out.Close(errors.New("partial"))

	assert.Equal(t, "Nothing to remove.\n", stdout.String())
	assert.Equal(t, "Error: partial\n", stderr.String())
}

// The final tree beside a text result keeps what the result does not say; a root's error is the
// Error line's alone.
func TestTTYOutput_Close_FinalTree(t *testing.T) {
	errRefused := errors.New("refused")
	tests := []struct {
		name       string
		result     any
		err        error
		wantStderr string
	}{
		{
			name:   "a result keeps every stage and leaves the warnings it repeats out",
			result: model.RemoveResponse{},
			wantStderr: "✔ savepoint.create    0.0s\n" +
				"├── ✔ volume.capture  0.0s\n" +
				"│     ! slow disk\n" +
				"└── ✔ compose.start   0.0s\n" +
				"\n",
		},
		{
			name: "no result writes the tree whole, the root's error once",
			err:  errRefused,
			wantStderr: "✘ savepoint.create    0.0s\n" +
				"  ! not covered: 1 service(s) without volumes\n" +
				"├── ✔ volume.capture  0.0s\n" +
				"│     ! slow disk\n" +
				"└── ✔ compose.start   0.0s\n" +
				"Error: refused\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				stdout, stderr bytes.Buffer
				out            = &ttyOutput{
					renderer: tty.New(&stderr),
					result:   resultOptions{format: formatText},
					stdout:   &stdout,
					stderr:   &stderr,
				}
				at      = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
				tracker = progress.New(out, func() time.Time { return at })
			)
			ctx, root := tracker.Begin(zerolog.Nop().WithContext(context.Background()), progress.Step{Kind: "savepoint.create"})
			root.WarnInResult("not covered: 1 service(s) without volumes")
			_, capture := progress.Run(ctx, progress.Step{Kind: "volume.capture"})
			capture.Warn("slow disk")
			capture.Done(progress.Sample{})
			_, start := progress.Run(ctx, progress.Step{Kind: "compose.start"})
			start.Done(progress.Sample{})
			root.End(tt.err)
			if tt.result != nil {
				require.NoError(t, out.Print(tt.result))
			}

			out.Close(tt.err)

			// The live frames come first; the final tree is what stays.
			got := ansi.Strip(stderr.String())
			assert.True(t, strings.HasSuffix(got, tt.wantStderr), "got:\n%s", got)
			assert.Equal(t, 1, strings.Count(got, tt.wantStderr), "the final tree is written once")
		})
	}
}

func TestComposeProgressMode(t *testing.T) {
	tests := []struct {
		name    string
		drawing bool
		mode    string
		want    string
	}{
		{
			name:    "compose's renderer gives way to arcave's tree",
			drawing: true,
			mode:    config.ProgressTTY,
			want:    config.ProgressLog,
		},
		{
			name:    "under the tree, another mode stands",
			drawing: true,
			mode:    config.ProgressQuiet,
			want:    config.ProgressQuiet,
		},
		{
			name: "with no tree, compose's renderer stands",
			mode: config.ProgressTTY,
			want: config.ProgressTTY,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := composeProgressMode(zerolog.Nop().WithContext(context.Background()), tt.mode, tt.drawing)

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestJSONOutput_Print_AResultThatCannotBeMarshalled(t *testing.T) {
	var buf bytes.Buffer
	out := newJSONOutput(&buf)

	err := out.Print(make(chan int))
	out.Close(err)

	require.Error(t, err)
	assert.Contains(t, buf.String(), `{"type":"error","error":"marshal the result: `)
}
