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

package compose

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

// progressLogMessage is the message of every record the adapter emits for a
// decoded event. The meaning lives in the fields, not in the message: the line
// then reads the same in text and in json format, and `msg=compose` is what a
// user greps for to isolate the library's own chatter from arcave's.
const progressLogMessage = "compose"

// maxProgressLine bounds the unterminated tail the adapter is willing to hold.
// compose writes one Fprintln per event, so the bound is never reached in
// practice; it is insurance against a stream that carries no newline at all,
// which would otherwise grow the buffer without limit. On overflow the
// accumulated bytes are emitted raw and the buffer is dropped.
const maxProgressLine = 64 << 10

// skippedStatusPrefix is what SkippedEvent puts in front of its reason. It is
// the one status the level table cannot hold as a key, since the reason is
// appended to it.
const skippedStatusPrefix = "Skipped: "

// progressLevels maps compose's status texts onto log levels. The severity
// compose itself attaches to an event (progress.Working/Done/Warning/Error) is
// not serialised — jsonMessage carries only StatusText — so the level is
// recovered from the text, and the full set of texts comes from the event
// constructors in the library's pkg/progress/event.go.
//
// The split is "in flight" versus "settled": a step that is still running is
// DEBUG, the outcome it settles into is INFO. A status this table does not know
// stays INFO; see the adapter's doc comment for why nothing guesses harder.
var progressLevels = map[string]zerolog.Level{
	"Creating":   zerolog.DebugLevel,
	"Starting":   zerolog.DebugLevel,
	"Waiting":    zerolog.DebugLevel,
	"Restarting": zerolog.DebugLevel,
	"Stopping":   zerolog.DebugLevel,
	"Killing":    zerolog.DebugLevel,
	"Removing":   zerolog.DebugLevel,
	"Building":   zerolog.DebugLevel,

	"Created":   zerolog.InfoLevel,
	"Started":   zerolog.InfoLevel,
	"Healthy":   zerolog.InfoLevel,
	"Exited":    zerolog.InfoLevel,
	"Restarted": zerolog.InfoLevel,
	"Running":   zerolog.InfoLevel,
	"Stopped":   zerolog.InfoLevel,
	"Killed":    zerolog.InfoLevel,
	"Removed":   zerolog.InfoLevel,
	"Built":     zerolog.InfoLevel,
	"Skipped":   zerolog.InfoLevel,

	"Error": zerolog.ErrorLevel,
}

// progressEvent is the subset of the compose library's jsonMessage this package
// reads. It is decoded leniently — no DisallowUnknownFields — because the schema
// is semi-public: a field added in the next compose release must not break the
// decode.
type progressEvent struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Total   int64  `json:"total"`
	Percent int    `json:"percent"`
}

// ProgressLogger is the stream compose renders its progress on, turned into
// arcave log records. compose is run in its JSON progress mode, so each event
// arrives as one JSON object per line; this adapter decodes them and re-emits
// each as a zerolog record with fields, which is what makes "one output channel"
// true even under log.format: json — where compose's own renderer would
// otherwise interleave raw text with the machine-readable stream.
//
// It has to be a writer rather than a progress.Writer implementation: compose's
// progress.RunWithStatus builds its own writer and puts it in the context,
// overwriting anything already there, so the only seam the library leaves is the
// global progress.Mode plus the io.Writer dockerCli.Err() points at.
//
// The adapter knows the event format, so it lives here rather than in the
// composition root: cmd/arcave only plugs the wire in.
//
// It is safe for concurrent use — compose's jsonWriter takes no lock of its own
// and its Events method is called from an errgroup, so two writes really can
// race. The mutex protects the line buffer, which is where a race would corrupt
// a record by splicing two half-events together. It does not order the records:
// concurrent events were unordered before this adapter too.
type ProgressLogger struct {
	logger zerolog.Logger

	mu  sync.Mutex
	buf bytes.Buffer
}

// NewProgressLogger returns the io.WriteCloser to hand compose as its progress
// stream. The logger is passed explicitly rather than taken from zerolog's
// global: the writer is built at the composition root, where the resolved logger
// already exists.
func NewProgressLogger(logger zerolog.Logger) *ProgressLogger {
	return &ProgressLogger{logger: logger}
}

// Write accumulates b and emits one record per complete line. A Write is not
// guaranteed to carry whole lines — compose writes one Fprintln per event today,
// but that is an observation, not a contract, and docker/cli's streams.Out sits
// between us and it — so an unterminated tail is held until the next Write.
//
// It never reports an error: refusing bytes would surface inside compose as a
// broken progress stream, and a log record arcave failed to render is not worth
// failing a savepoint over.
func (p *ProgressLogger) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.buf.Write(b)
	for {
		line, err := p.buf.ReadString('\n')
		if err != nil {
			// io.EOF: line is the tail with no newline yet. Put it back and wait
			// for the rest of it.
			p.buf.Reset()
			p.buf.WriteString(line)
			break
		}
		p.emit(line)
	}
	if p.buf.Len() > maxProgressLine {
		p.emit(p.buf.String())
		p.buf.Reset()
	}
	return len(b), nil
}

// Close emits whatever the last Write left unterminated. Without it a final line
// written without a trailing newline would sit in the buffer forever. It is
// called from (*dependencies).Close alongside the other teardown, and returns no
// error worth acting on.
func (p *ProgressLogger) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.buf.Len() > 0 {
		p.emit(p.buf.String())
		p.buf.Reset()
	}
	return nil
}

// emit turns one line into one record. Anything that is not a JSON object goes
// through verbatim: compose writes to this same stream with a plain fmt.Fprintln
// in a few places (kill.go, remove.go, build.go's --memory warning), and those
// lines stay text even in JSON progress mode. A line that starts like JSON but
// does not decode is treated the same way — a decode failure is not an error
// here, it is a line arcave does not recognise.
func (p *ProgressLogger) emit(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if !strings.HasPrefix(line, "{") {
		p.raw(line)
		return
	}

	var event progressEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		p.raw(line)
		return
	}

	record := p.logger.WithLevel(progressLevel(event))
	if kind, name := splitProgressID(event.ID); name != "" {
		if kind != "" {
			record = record.Str("kind", kind)
		}
		record = record.Str("name", name)
	}
	if event.Status != "" {
		record = record.Str("status", event.Status)
	}
	// Lifecycle events carry no text at all, so writing it unconditionally would
	// put an empty field on every line.
	if event.Text != "" {
		record = record.Str("text", event.Text)
	}
	// Percent is only meaningful while a total is known — an image layer's byte
	// progress. A lifecycle event leaves Total zero and Percent at whatever the
	// library last set, so gating on Total is what keeps a stray percent=100 off
	// a "Healthy" line.
	if event.Total > 0 {
		record = record.Int("percent", event.Percent)
	}
	record.Msg(progressLogMessage)
}

// raw re-emits a line compose wrote outside its event stream. The line itself
// becomes the message, since there are no fields to carry it, and source=compose
// marks it as the library's words rather than arcave's. It stays at INFO rather
// than guessing at severity from the text.
func (p *ProgressLogger) raw(line string) {
	p.logger.Info().Str("source", "compose").Msg(line)
}

// progressLevel picks the level for one event. Byte-progress ticks are demoted
// wholesale: pull.go fills Current/Total/Percent from the daemon's own progress
// messages, and those arrive many times per layer. Demoting them by *content*
// rather than by remembering the previous status is what keeps this adapter
// stateless — the alternative, an id-to-status map, is deduplication by another
// name, and duplicate lifecycle events (compose reports Waiting and Healthy
// twice per Up) are reported honestly instead.
//
// The terminal "Pull complete" / "Already exists" events arrive with no total,
// so they stay at INFO: one visible line per layer, none of the ticks.
func progressLevel(event progressEvent) zerolog.Level {
	if event.Total > 0 {
		return zerolog.DebugLevel
	}
	if strings.HasPrefix(event.Status, skippedStatusPrefix) {
		return zerolog.WarnLevel
	}
	if level, ok := progressLevels[event.Status]; ok {
		return level
	}
	// An unknown status is reported, not guessed at. ErrorMessageEvent puts a
	// free-form message here and will land on INFO; that is cosmetic, since the
	// failure itself still comes back from Up and arcave logs it at ERROR in its
	// own right. A "contains error/failed → WARN" heuristic would be guesswork,
	// and guesswork in a log is worse than an honest INFO.
	return zerolog.InfoLevel
}

// splitProgressID splits compose's event id — "Container arcave-demo-app-1" —
// into the kind of object and its name. An id with no space is all name: the
// kind is a convention of the library's own event constructors, not a guarantee.
func splitProgressID(id string) (kind, name string) {
	kind, name, found := strings.Cut(id, " ")
	if !found {
		return "", kind
	}
	return kind, name
}
