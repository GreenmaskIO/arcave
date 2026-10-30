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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/colorprofile"
	"github.com/rs/zerolog"
	"golang.org/x/term"

	jsonprinter "github.com/greenmaskio/arcave/internal/service/printer/json"
	textprinter "github.com/greenmaskio/arcave/internal/service/printer/text"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
	"github.com/greenmaskio/arcave/pkg/progress/reporter"
	"github.com/greenmaskio/arcave/pkg/progress/reporter/tty"
)

const (
	// progressEnv is the environment counterpart of --progress.
	progressEnv = "ARCAVE_PROGRESS"

	progressModeAuto = "auto"
	progressModeTTY  = "tty"
	progressModeLog  = "log"
	progressModeJSON = "json"

	// formatEnv is the environment counterpart of --format.
	formatEnv = "ARCAVE_FORMAT"

	formatAuto = "auto"
	formatJSON = "json"
	formatText = "text"

	// moreWarningsHint follows the count of the warnings the final tree leaves out of a node.
	moreWarningsHint = "--progress log shows all"

	outputLineProgress = "progress"
	outputLineResult   = "result"
	outputLineError    = "error"
)

// openedOutput holds the output initRoot opens and main closes; the signal handler reads it from
// its own goroutine.
var openedOutput atomic.Value

// loadRunOutput is the run's output, nil before initRoot opens it.
func loadRunOutput() output {
	if out, ok := openedOutput.Load().(output); ok {
		return out
	}
	return nil
}

// outputClosed lets one caller close the output: one racing it waits for that close to finish.
var outputClosed sync.Once

// output is a run's one channel: progress while it runs, the result once it is done.
type output interface {
	progress.Reporter
	// Print takes the run's result; it is cli's printer seam.
	Print(v any) error
	// Close ends the stream, err being what the run failed with, nil for none.
	Close(err error)
}

// resultPrinter prints a run's result.
type resultPrinter interface {
	Print(v any) error
}

// resultOptions are how a run prints its result: the format, the -p and --session the text
// spells its next commands with, and the colors stdout takes.
type resultOptions struct {
	format         string
	composeProject string
	session        string
	profile        colorprofile.Profile
}

// newPrinter returns the printer of the format, writing to w.
func (o resultOptions) newPrinter(w io.Writer) resultPrinter {
	if o.format == formatText {
		return textprinter.New(w, textprinter.Options{
			ComposeProject: o.composeProject,
			Session:        o.session,
			Profile:        o.profile,
		})
	}
	return jsonprinter.New(w)
}

// resolveProgressMode turns --progress into the mode a run renders in: auto is tty on a
// terminal stderr with text logs, else log.
func resolveProgressMode(mode string, stderrTerminal bool, logFormat string) (string, error) {
	switch mode {
	case progressModeTTY, progressModeLog, progressModeJSON:
		return mode, nil
	case progressModeAuto, "":
		if stderrTerminal && logFormat == utils.LogFormatText {
			return progressModeTTY, nil
		}
		return progressModeLog, nil
	default:
		return "", fmt.Errorf("--progress %q: want %s, %s, %s or %s",
			mode, progressModeAuto, progressModeTTY, progressModeLog, progressModeJSON)
	}
}

// resolveFormat turns --format into the format the result is printed in: auto is text only while
// the tree is drawn and stdout is a terminal too, so a captured stdout still reads JSON.
func resolveFormat(format, progressMode string, stdoutTerminal bool) (string, error) {
	switch format {
	case formatText:
		if progressMode == progressModeJSON {
			return "", fmt.Errorf("--format %s: the result of --progress %s is the stream's last JSON line",
				formatText, progressModeJSON)
		}
		return formatText, nil
	case formatJSON:
		return formatJSON, nil
	case formatAuto, "":
		if progressMode == progressModeTTY && stdoutTerminal {
			return formatText, nil
		}
		return formatJSON, nil
	default:
		return "", fmt.Errorf("--format %q: want %s, %s or %s", format, formatAuto, formatJSON, formatText)
	}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// openOutput builds the output of mode, printing the result as result says, and installs the
// logger writing beside it at level; with no level set, only warnings show above the live tree.
func openOutput(mode, level, logFormat string, result resultOptions) (output, zerolog.Logger, error) {
	level = resolveLogLevel(mode, level)
	if mode != progressModeTTY {
		logger, err := utils.SetDefaultContextLogger(level, logFormat, os.Stderr)
		if err != nil {
			return nil, zerolog.Logger{}, err
		}
		if mode == progressModeJSON {
			return newJSONOutput(os.Stdout), logger, nil
		}
		return &logOutput{
			reporter: reporter.NewLog(logger),
			printer:  result.newPrinter(os.Stdout),
			stderr:   os.Stderr,
		}, logger, nil
	}

	out := &ttyOutput{
		reporter: tty.New(os.Stderr),
		result:   result,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
	}
	logger, err := utils.SetDefaultContextLogger(level, logFormat, out)
	if err != nil {
		return nil, zerolog.Logger{}, err
	}
	return out, logger, nil
}

// resolveLogLevel is the level a run of mode logs at: the one set, else warn above the live tree
// and info in a log.
func resolveLogLevel(mode, level string) string {
	switch {
	case level != "":
		return level
	case mode == progressModeTTY:
		return zerolog.LevelWarnValue
	default:
		return utils.DefaultLogLevel
	}
}

// closeOutput ends out with err, or writes err as cobra would when no output was opened; only the
// first call of a run does, and a concurrent one returns once it has.
func closeOutput(out output, err error) {
	outputClosed.Do(func() {
		if out == nil {
			writeError(os.Stderr, err)
			return
		}
		out.Close(err)
	})
}

// writeError writes err as cobra would, nothing for nil.
func writeError(w io.Writer, err error) {
	if err == nil {
		return
	}
	//nolint:errcheck // stderr that fails a write has nowhere to report it
	_, _ = fmt.Fprintln(w, "Error:", err)
}

// logOutput reports progress as log records and prints the result through its printer.
type logOutput struct {
	reporter *reporter.Log
	printer  resultPrinter
	stderr   io.Writer
}

func (o *logOutput) Report(evt progress.Event) {
	o.reporter.Report(evt)
}

func (o *logOutput) Print(v any) error {
	return o.printer.Print(v)
}

func (o *logOutput) Close(err error) {
	writeError(o.stderr, err)
}

// ttyOutput draws progress as a live tree on stderr from the first event, and writes the result to
// stdout at Close, once the tree is down.
type ttyOutput struct {
	reporter *tty.Reporter
	result   resultOptions
	stdout   io.Writer
	stderr   io.Writer

	mu      sync.Mutex
	printed []byte
}

func (o *ttyOutput) Report(evt progress.Event) {
	o.reporter.Report(evt)
}

// Write is the log's writer: straight to stderr until the tree is drawn, above it after.
func (o *ttyOutput) Write(p []byte) (int, error) {
	return o.reporter.LogWriter().Write(p)
}

// Print renders v for Close to write: a partial gc prints its result and still fails.
func (o *ttyOutput) Print(v any) error {
	var buf bytes.Buffer
	if err := o.result.newPrinter(&buf).Print(v); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.printed = buf.Bytes()
	return nil
}

// Close takes the live tree down, leaving the final one in the scrollback, then writes the result
// and err.
func (o *ttyOutput) Close(err error) {
	o.mu.Lock()
	printed := o.printed
	o.printed = nil
	o.mu.Unlock()
	essential := o.result.format == formatText && printed != nil
	drawn := o.reporter.Stop(tty.StopOptions{
		Essential:     essential,
		OmitRootError: err != nil,
		MoreHint:      moreWarningsHint,
	})
	if drawn && essential {
		// The tree and the result below it are two blocks.
		//nolint:errcheck // stderr that fails a write has nowhere to report it
		_, _ = fmt.Fprintln(o.stderr)
	}
	if printed != nil {
		//nolint:errcheck // stdout that fails a write has nowhere to report it
		_, _ = o.stdout.Write(printed)
	}
	writeError(o.stderr, err)
}

// outputLine is one line of the json stream: an event, the result, or the error, which a
// partial result carries beside it.
type outputLine struct {
	Type   string          `json:"type"`
	Event  *progress.Event `json:"event,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// jsonOutput writes the whole run to stdout as JSON lines: every event, then one last line
// with the result or the error.
type jsonOutput struct {
	mu     sync.Mutex
	enc    *json.Encoder
	result json.RawMessage
}

func newJSONOutput(w io.Writer) *jsonOutput {
	return &jsonOutput{enc: json.NewEncoder(w)}
}

func (o *jsonOutput) Report(evt progress.Event) {
	o.write(outputLine{
		Type:  outputLineProgress,
		Event: &evt,
	})
}

// Print keeps v for the last line, which Close writes: a partial result goes out with its error.
// A result that cannot be marshalled fails here, so the run fails and the last line says why.
func (o *jsonOutput) Print(v any) error {
	result, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal the result: %w", err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.result = result
	return nil
}

func (o *jsonOutput) Close(err error) {
	o.mu.Lock()
	result := o.result
	o.mu.Unlock()
	var errText string
	if err != nil {
		errText = err.Error()
	}
	switch {
	case result != nil:
		o.write(outputLine{
			Type:   outputLineResult,
			Result: result,
			Error:  errText,
		})
	case err != nil:
		o.write(outputLine{
			Type:  outputLineError,
			Error: errText,
		})
	}
}

func (o *jsonOutput) write(line outputLine) {
	o.mu.Lock()
	defer o.mu.Unlock()
	//nolint:errcheck // stdout that fails a write has nowhere to report it
	_ = o.enc.Encode(line)
}
