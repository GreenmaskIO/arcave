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
	"context"
	"strings"
	"sync"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/rs/zerolog"
)

// progressLogMessage is the message of every record the adapter emits for an
// event. The meaning lives in the fields, not in the message: the line then
// reads the same in text and in json format, and `msg=compose` is what a user
// greps for to isolate the library's own chatter from arcave's.
const progressLogMessage = "compose"

// maxProgressLine bounds the unterminated tail the adapter is willing to hold.
// compose writes one Fprintln per line, so the bound is never reached in
// practice; it is insurance against a stream that carries no newline at all,
// which would otherwise grow the buffer without limit. On overflow the
// accumulated bytes are emitted and the buffer is dropped.
const maxProgressLine = 64 << 10

// progressLevels maps the severity compose attaches to an event onto a log
// level. The split is "in flight" versus "settled": a step that is still running
// is DEBUG, the outcome it settles into is INFO.
var progressLevels = map[api.EventStatus]zerolog.Level{
	api.Working: zerolog.DebugLevel,
	api.Done:    zerolog.InfoLevel,
	api.Warning: zerolog.WarnLevel,
	api.Error:   zerolog.ErrorLevel,
}

// ProgressLogger turns compose's progress into arcave log records, which is what
// makes "one output channel" true even under log.format: json — where compose's
// own renderer would otherwise interleave raw text with the machine-readable
// stream.
//
// It has two faces. As an api.EventProcessor it receives compose's events as
// structures and re-emits each as a zerolog record with fields. As an
// io.WriteCloser it is the error stream of the docker CLI compose runs over:
// compose still writes a few lines there with a plain fmt.Fprintln (remove.go's
// "No stopped containers", for one), and those go through verbatim.
//
// The adapter knows the event shape, so it lives here rather than in the
// composition root: cmd/arcave only plugs the wire in.
//
// It is safe for concurrent use — compose reports events from an errgroup, and
// its stream writes take no lock of their own. The mutex protects the line
// buffer, which is where a race would corrupt a record by splicing two half-lines
// together. It does not order the records: concurrent events were unordered
// before this adapter too.
type ProgressLogger struct {
	logger zerolog.Logger

	mu  sync.Mutex
	buf bytes.Buffer
}

// NewProgressLogger returns the adapter to hand compose as both its event
// processor and its error stream. The logger is passed explicitly rather than
// taken from zerolog's global: the adapter is built at the composition root,
// where the resolved logger already exists.
func NewProgressLogger(logger zerolog.Logger) *ProgressLogger {
	return &ProgressLogger{logger: logger}
}

// Start is a no-op: an operation's start carries nothing its events do not.
func (p *ProgressLogger) Start(context.Context, string) {}

// Done is a no-op: the outcome of an operation comes back from the call itself,
// which arcave reports in its own right.
func (p *ProgressLogger) Done(string, bool) {}

// On emits one record per event.
func (p *ProgressLogger) On(events ...api.Resource) {
	for _, event := range events {
		p.event(event)
	}
}

func (p *ProgressLogger) event(event api.Resource) {
	record := p.logger.WithLevel(progressLevel(event))
	if kind, name := splitProgressID(event.ID); name != "" {
		if kind != "" {
			record = record.Str("kind", kind)
		}
		record = record.Str("name", name)
	}
	// Text is the step ("Created", "Pull complete"), Details what compose adds to
	// it; a lifecycle event carries no details, so writing them unconditionally
	// would put an empty field on every line.
	if event.Text != "" {
		record = record.Str("status", event.Text)
	}
	if event.Details != "" {
		record = record.Str("text", event.Details)
	}
	// Percent is only meaningful while a total is known — an image layer's byte
	// progress. A completed layer leaves Total zero and Percent at 100, so gating
	// on Total is what keeps a stray percent=100 off a "Pull complete" line.
	if event.Total > 0 {
		record = record.Int("percent", event.Percent)
	}
	record.Msg(progressLogMessage)
}

// progressLevel picks the level for one event. Byte-progress ticks are demoted
// wholesale: pull.go fills Current/Total/Percent from the daemon's own progress
// messages, and those arrive many times per layer. Demoting them by content
// rather than by remembering the previous status is what keeps this adapter
// stateless. The terminal "Pull complete" / "Already exists" events arrive with
// no total, so they stay at INFO: one visible line per layer, none of the ticks.
func progressLevel(event api.Resource) zerolog.Level {
	if event.Total > 0 {
		return zerolog.DebugLevel
	}
	if level, ok := progressLevels[event.Status]; ok {
		return level
	}
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

// Write accumulates b and emits one record per complete line. A Write is not
// guaranteed to carry whole lines — docker/cli's streams.Out sits between us
// and compose — so an unterminated tail is held until the next Write.
//
// It never reports an error: refusing bytes would surface inside compose as a
// broken stream, and a log record arcave failed to render is not worth failing
// a savepoint over.
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
		p.raw(line)
	}
	if p.buf.Len() > maxProgressLine {
		p.raw(p.buf.String())
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
		p.raw(p.buf.String())
		p.buf.Reset()
	}
	return nil
}

// raw re-emits a line compose wrote outside its event stream. The line itself
// becomes the message, since there are no fields to carry it, and source=compose
// marks it as the library's words rather than arcave's. It stays at INFO rather
// than guessing at severity from the text.
func (p *ProgressLogger) raw(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	p.logger.Info().Str("source", "compose").Msg(line)
}
