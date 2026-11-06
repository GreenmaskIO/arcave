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

// EventState is where one event leaves its object.
type EventState int

const (
	// EventWorking is a step still under way.
	EventWorking EventState = iota
	// EventDone is the outcome a step settled into.
	EventDone
	// EventWarning is a step compose skipped.
	EventWarning
	// EventError is a step that failed.
	EventError
)

// Event is one compose progress event.
type Event struct {
	// Kind is compose's kind of object — Container, Network, Volume, Image — empty when the id
	// names none.
	Kind   string
	Name   string
	Status string
	Text   string
	State  EventState
}

// EventSink takes the adapter's events in place of the log while it is set.
type EventSink interface {
	Event(evt Event)
}

// progressStates maps the severity compose attaches to an event onto the state
// it leaves the object in.
var progressStates = map[api.EventStatus]EventState{
	api.Working: EventWorking,
	api.Done:    EventDone,
	api.Warning: EventWarning,
	api.Error:   EventError,
}

// stateLevels is the level a state is logged at: a step in flight is DEBUG, the outcome it
// settles into INFO.
var stateLevels = map[EventState]zerolog.Level{
	EventWorking: zerolog.DebugLevel,
	EventDone:    zerolog.InfoLevel,
	EventWarning: zerolog.WarnLevel,
	EventError:   zerolog.ErrorLevel,
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
//
// While a sink is set, an event goes to it instead of the log: compose.Client sets the one its
// call's context carries for the call's duration.
type ProgressLogger struct {
	logger zerolog.Logger

	mu   sync.Mutex
	buf  bytes.Buffer
	sink EventSink
}

// NewProgressLogger returns the adapter to hand compose as both its event
// processor and its error stream. The logger is passed explicitly rather than
// taken from zerolog's global: the adapter is built at the composition root,
// where the resolved logger already exists.
func NewProgressLogger(logger zerolog.Logger) *ProgressLogger {
	return &ProgressLogger{logger: logger}
}

// SetSink routes events to sink, or back to the log for nil.
func (p *ProgressLogger) SetSink(sink EventSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sink = sink
}

// Start is a no-op: an operation's start carries nothing its events do not.
func (p *ProgressLogger) Start(context.Context, string) {}

// Done is a no-op: the outcome of an operation comes back from the call itself,
// which arcave reports in its own right.
func (p *ProgressLogger) Done(string, bool) {}

// On emits one record per event, or hands each to the sink while one is set.
func (p *ProgressLogger) On(events ...api.Resource) {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()

	for _, event := range events {
		if sink != nil {
			kind, name := splitProgressID(event.ID)
			sink.Event(Event{
				Kind:   kind,
				Name:   name,
				Status: event.Text,
				Text:   event.Details,
				State:  progressState(event),
			})
			continue
		}
		p.event(event)
	}
}

func (p *ProgressLogger) event(event api.Resource) {
	record := p.logger.WithLevel(stateLevels[progressState(event)])
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

// progressState picks the state of one event. Byte-progress ticks are in flight
// wholesale: pull.go fills Current/Total/Percent from the daemon's own progress
// messages, and those arrive many times per layer. Judging them by content
// rather than by remembering the previous status is what keeps this adapter
// stateless. The terminal "Pull complete" / "Already exists" events arrive with
// no total, so they are done: one visible line per layer, none of the ticks.
func progressState(event api.Resource) EventState {
	if event.Total > 0 {
		return EventWorking
	}
	if state, ok := progressStates[event.Status]; ok {
		return state
	}
	return EventDone
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
