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
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// composeVolumePrompt is compose v5.7.0's own text, copied rather than built from the constant:
// a compose that rewords it turns this red.
const composeVolumePrompt = `Volume "demo_data" exists but doesn't match configuration in compose file. Recreate (data will be lost)?`

func TestParseVolumeRecreatePrompt(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
		wantOK  bool
	}{
		{name: "compose's text", message: composeVolumePrompt, want: "demo_data", wantOK: true},
		{
			name:    "a name with a quote in it",
			message: `Volume "a\"b" exists but doesn't match configuration in compose file. Recreate (data will be lost)?`,
			want:    `a"b`,
			wantOK:  true,
		},
		{name: "another question", message: "Continue?"},
		{name: "the text with a tail", message: composeVolumePrompt + " [y/N]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, ok := parseVolumeRecreatePrompt(tt.message)

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPrompter_Prompt(t *testing.T) {
	tests := []struct {
		name         string
		message      string
		defaultValue bool
		call         *promptCall
		withSink     bool
		want         bool
		wantEvents   []Event
		wantLog      string
	}{
		{
			name:    "a volume the call restores is recreated",
			message: composeVolumePrompt,
			call:    &promptCall{restored: []string{"demo_data"}},
			want:    true,
			wantLog: `{"level":"info","volume":"demo_data"`,
		},
		{
			name:     "a volume the call does not restore is kept, warned of on the step",
			message:  composeVolumePrompt,
			call:     &promptCall{restored: []string{"demo_other"}},
			withSink: true,
			want:     false,
			wantEvents: []Event{{
				Kind:  "Volume",
				Name:  "demo_data",
				Text:  "config differs from the savepoint's; not recreated, as the savepoint does not restore it",
				State: EventWarning,
			}},
		},
		{
			name:    "a volume the call does not restore is kept, warned of in the log",
			message: composeVolumePrompt,
			call:    &promptCall{},
			want:    false,
			wantLog: `{"level":"warn","volume":"demo_data"`,
		},
		{
			name:    "outside a call nothing is recreated",
			message: composeVolumePrompt,
			want:    false,
			wantLog: `{"level":"warn","volume":"demo_data"`,
		},
		{
			name:         "another question gets its default",
			message:      "Continue?",
			defaultValue: true,
			call:         &promptCall{},
			want:         true,
			wantLog:      `{"level":"warn","prompt":"Continue?"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			prompter := NewPrompter(logger)
			sink := &sinkRecorder{}
			if tt.call != nil {
				call := *tt.call
				call.logger = &logger
				if tt.withSink {
					call.sink = sink
				}
				defer prompter.begin(call)()
			}

			// Act
			got, err := prompter.Prompt(tt.message, tt.defaultValue)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantEvents, sink.events)
			if tt.wantLog == "" {
				assert.Empty(t, logs.String())
			} else {
				assert.Contains(t, logs.String(), tt.wantLog)
			}
		})
	}
}

// Create answers compose's questions by the volumes it is handed, warning on the call's step of
// one it keeps, for its own duration only.
func TestClient_Create_AnswersPromptsByTheRestoredVolumes(t *testing.T) {
	// Arrange
	const keptPrompt = `Volume "demo_kept" exists but doesn't match configuration in compose file. Recreate (data will be lost)?`
	project := fixtureProject()
	prompter := NewPrompter(zerolog.Nop())
	sink := &sinkRecorder{}
	var restoredDuring, keptDuring bool
	composeAPI := &mockComposeAPI{}
	composeAPI.On("Create", mock.Anything,
		mock.MatchedBy(func(p *types.Project) bool { return p.Name == project.Name }),
		mock.MatchedBy(func(o api.CreateOptions) bool { return o.Recreate == api.RecreateNever && !o.Inherit }),
	).
		Run(func(mock.Arguments) {
			restoredDuring, _ = prompter.Prompt(composeVolumePrompt, false)
			keptDuring, _ = prompter.Prompt(keptPrompt, false)
		}).
		Return(nil).Once()
	opts := testOptions
	opts.Prompter = prompter
	opts.SinkFromContext = func(context.Context) EventSink { return sink }
	c := NewClient(composeAPI, opts)

	// Act
	err := c.Create(context.Background(), project, []string{"demo_data"})
	restoredAfter, _ := prompter.Prompt(composeVolumePrompt, false)
	keptAfter, _ := prompter.Prompt(keptPrompt, false)

	// Assert
	require.NoError(t, err)
	assert.True(t, restoredDuring, "the restored volume is recreated during the call")
	assert.False(t, keptDuring, "a volume the call does not restore is kept")
	assert.False(t, restoredAfter, "past the call no volume is restored")
	assert.False(t, keptAfter)
	assert.Equal(t, []Event{{
		Kind:  "Volume",
		Name:  "demo_kept",
		Text:  "config differs from the savepoint's; not recreated, as the savepoint does not restore it",
		State: EventWarning,
	}}, sink.events, "the call's step is warned once, during the call")
	composeAPI.AssertExpectations(t)
}
