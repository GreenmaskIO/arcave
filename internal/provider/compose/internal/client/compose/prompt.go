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
	"fmt"
	"slices"
	"sync"

	"github.com/rs/zerolog"
)

// volumeRecreatePrompt is compose's question before it recreates a volume whose config drifted
// (pkg/compose/reconcile.go, v5.7.0).
const volumeRecreatePrompt = "Volume %q exists but doesn't match configuration in compose file. Recreate (data will be lost)?"

// volumeKind is compose's kind of a volume event.
const volumeKind = "Volume"

// Prompter answers compose's questions: a drifted volume is recreated only when the call restores
// it, and kept, with a warning, otherwise. It never writes to stdout.
type Prompter struct {
	logger zerolog.Logger

	mu   sync.Mutex
	call promptCall
}

// promptCall is what one Client call answers by: the volumes it restores, where it warns.
type promptCall struct {
	restored []string
	sink     EventSink
	logger   *zerolog.Logger
}

// NewPrompter returns the prompter to hand compose.WithPrompt; logger takes what no call does.
func NewPrompter(logger zerolog.Logger) *Prompter {
	return &Prompter{logger: logger}
}

// Prompt is compose's Prompt: it never fails.
func (p *Prompter) Prompt(message string, defaultValue bool) (bool, error) {
	p.mu.Lock()
	call := p.call
	p.mu.Unlock()

	logger := &p.logger
	if call.logger != nil {
		logger = call.logger
	}
	volume, ok := parseVolumeRecreatePrompt(message)
	if !ok {
		logger.Warn().Str("prompt", message).Bool("answer", defaultValue).Msg("compose asked a question arcave does not know")
		return defaultValue, nil
	}
	if slices.Contains(call.restored, volume) {
		logger.Info().Str("volume", volume).Msg("recreating a volume whose config differs: the savepoint restores it")
		return true, nil
	}

	msg := "config differs from the savepoint's; not recreated, as the savepoint does not restore it"
	if call.sink != nil {
		call.sink.Event(Event{
			Kind:  volumeKind,
			Name:  volume,
			Text:  msg,
			State: EventWarning,
		})
	} else {
		logger.Warn().Str("volume", volume).Msgf("volume %s: %s", volume, msg)
	}
	return false, nil
}

// begin installs call for the duration of one Client call; the returned func takes it back.
func (p *Prompter) begin(call promptCall) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.call = call
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.call = promptCall{}
	}
}

// parseVolumeRecreatePrompt is the volume compose's recreate question names.
func parseVolumeRecreatePrompt(message string) (string, bool) {
	var volume string
	if _, err := fmt.Sscanf(message, volumeRecreatePrompt, &volume); err != nil {
		return "", false
	}
	if fmt.Sprintf(volumeRecreatePrompt, volume) != message {
		return "", false
	}
	return volume, true
}
