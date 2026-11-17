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
	"context"
	"strings"
	"sync"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/rs/zerolog"
)

// progressLogMessage is the message of every record the processor logs; the meaning is in the
// fields, and `msg=compose` isolates the library's chatter from arcave's.
const progressLogMessage = "compose"

// EventState is where one event leaves its object.
type EventState int

const (
	// EventWorking is a step still under way.
	EventWorking EventState = iota
	// EventDone is the outcome a step settled into.
	EventDone
	// EventWarning is a step compose skipped or warned of.
	EventWarning
	// EventError is a step that failed.
	EventError
)

// Event is one compose progress event.
type Event struct {
	// Kind is compose's kind of object — Container, Network, Volume, Image — empty when the id
	// names none.
	Kind string
	Name string
	// Status is compose's status text (api.Status*), Text its details.
	Status string
	Text   string
	State  EventState
}

// EventSink takes the processor's events in place of the log while it is set.
type EventSink interface {
	Event(evt Event)
}

// eventStates maps the severity compose attaches to an event onto the state it leaves its object in.
var eventStates = map[api.EventStatus]EventState{
	api.Working: EventWorking,
	api.Done:    EventDone,
	api.Warning: EventWarning,
	api.Error:   EventError,
}

// stateLevels is the level a state is logged at: a step in flight is DEBUG, its outcome INFO.
var stateLevels = map[EventState]zerolog.Level{
	EventWorking: zerolog.DebugLevel,
	EventDone:    zerolog.InfoLevel,
	EventWarning: zerolog.WarnLevel,
	EventError:   zerolog.ErrorLevel,
}

// EventLogger is compose's api.EventProcessor: each event goes to the sink while one is set,
// else to the log. Safe for concurrent use: compose reports from an errgroup.
type EventLogger struct {
	logger zerolog.Logger

	mu   sync.Mutex
	sink EventSink
}

// NewEventLogger returns the processor to hand compose.WithEventProcessor.
func NewEventLogger(logger zerolog.Logger) *EventLogger {
	return &EventLogger{logger: logger}
}

// SetSink routes events to sink, or back to the log for nil.
func (p *EventLogger) SetSink(sink EventSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sink = sink
}

// Start logs that compose began an operation.
func (p *EventLogger) Start(_ context.Context, operation string) {
	p.logger.Debug().Str("operation", operation).Msg("compose operation started")
}

// On reports each event to the sink, or logs it.
func (p *EventLogger) On(events ...api.Resource) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, resource := range events {
		p.emit(resource)
	}
}

// Done logs that compose ended an operation.
func (p *EventLogger) Done(operation string, success bool) {
	p.logger.Debug().Str("operation", operation).Bool("success", success).Msg("compose operation done")
}

// emit turns one resource event into a sink event or a record.
func (p *EventLogger) emit(resource api.Resource) {
	var (
		kind, name = splitProgressID(resource.ID)
		state      = newEventState(resource)
	)
	if p.sink != nil {
		p.sink.Event(Event{
			Kind:   kind,
			Name:   name,
			Status: resource.Text,
			Text:   resource.Details,
			State:  state,
		})
		return
	}

	record := p.logger.WithLevel(stateLevels[state])
	if name != "" {
		if kind != "" {
			record = record.Str("kind", kind)
		}
		record = record.Str("name", name)
	}
	if resource.Text != "" {
		record = record.Str("status", resource.Text)
	}
	if resource.Details != "" {
		record = record.Str("text", resource.Details)
	}
	// Percent means something only while a total is known: an image layer's byte progress.
	if resource.Total > 0 {
		record = record.Int("percent", resource.Percent)
	}
	record.Msg(progressLogMessage)
}

// newEventState is the state of one event; an unknown severity is an error, as compose renders it.
func newEventState(resource api.Resource) EventState {
	if state, ok := eventStates[resource.Status]; ok {
		return state
	}
	return EventError
}

// splitProgressID splits compose's event id — "Container arcave-demo-app-1" — into the kind of
// object and its name. An id with no space is all name.
func splitProgressID(id string) (kind, name string) {
	kind, name, found := strings.Cut(id, " ")
	if !found {
		return "", kind
	}
	return kind, name
}
