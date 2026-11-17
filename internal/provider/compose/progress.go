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
	"io"
	"os"
	"strings"

	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/rs/zerolog"
	"golang.org/x/term"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composeclient "github.com/greenmaskio/arcave/internal/provider/compose/internal/client/compose"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// composeResourceObject is the resource kind of a compose event whose id names no kind of object.
const composeResourceObject = "object"

// composeProgress is where compose's own progress goes.
type composeProgress struct {
	// processor is what compose reports its events to.
	processor api.EventProcessor
	// stream is the CLI's error stream, which compose writes beside its events.
	stream io.Writer
	// events re-emits the events as log records, or on a step; nil when compose renders them itself.
	events *composeclient.EventLogger
}

// newComposeProgress chooses compose's renderer: its own on stderr, nothing, or by default the
// logger re-emitting its events as log records, which a call's step can take them from.
func newComposeProgress(mode string, logger zerolog.Logger) composeProgress {
	switch mode {
	case composeconfig.ProgressQuiet:
		// Discarding the stream too covers the warnings compose writes outside the UI.
		return composeProgress{
			processor: display.Quiet(),
			stream:    io.Discard,
		}
	case composeconfig.ProgressTTY:
		processor := display.Plain(os.Stderr)
		if term.IsTerminal(int(os.Stderr.Fd())) {
			processor = display.Full(os.Stderr, os.Stderr, false)
		}
		return composeProgress{
			processor: processor,
			stream:    os.Stderr,
		}
	default:
		events := composeclient.NewEventLogger(logger)
		return composeProgress{
			processor: events,
			stream:    io.Discard,
			events:    events,
		}
	}
}

// composeEventSink puts compose's events on the progress step of the call, a resource per object.
type composeEventSink struct {
	step *progress.StepReporter
}

// newComposeEventSink is the sink of the step ctx carries; nil under a mute one, so the events
// stay in the log.
func newComposeEventSink(ctx context.Context) composeclient.EventSink {
	step := progress.Current(ctx)
	if step.IsMute() {
		return nil
	}
	return composeEventSink{step: step}
}

// Event reports one event as its object's line: in flight, settled, or a warning.
func (s composeEventSink) Event(evt composeclient.Event) {
	kind := strings.ToLower(evt.Kind)
	if kind == "" {
		kind = composeResourceObject
	}
	var (
		resource = progress.Resource{
			Kind: progress.ResourceKind(kind),
			ID:   evt.Name,
		}
		msg = strings.TrimSpace(evt.Status + " " + evt.Text)
	)
	switch evt.State {
	case composeclient.EventWorking:
		s.step.UpdateResource(resource, msg)
	case composeclient.EventDone:
		s.step.SettleResource(resource, msg)
	default:
		s.step.WarnResource(resource, msg)
	}
}
