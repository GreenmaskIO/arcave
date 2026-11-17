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

package capturepolicy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// hookDefaults are the defaults with the config's hook settings.
var hookDefaults = func() CaptureDefaults {
	withHooks := defaults
	withHooks.HookTimeout = time.Minute
	withHooks.HookOnError = model.HookOnErrorFail
	return withHooks
}()

// A service's hooks are its labels, each setting a label leaves unset the config's.
func TestResolve_Hooks(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		wantPre  *model.Hook
		wantPost *model.Hook
	}{
		{
			name: "no label: no hook",
		},
		{
			name:   "a pre-capture hook by the config's settings",
			labels: map[string]string{"arcave.hook.pre-capture": "pg_ctl checkpoint"},
			wantPre: &model.Hook{
				Command: []string{"pg_ctl", "checkpoint"},
				Timeout: time.Minute,
				OnError: model.HookOnErrorFail,
			},
		},
		{
			name:   "a post-capture hook alone",
			labels: map[string]string{"arcave.hook.post-capture": "/app/bin/maintenance off"},
			wantPost: &model.Hook{
				Command: []string{"/app/bin/maintenance", "off"},
				Timeout: time.Minute,
				OnError: model.HookOnErrorFail,
			},
		},
		{
			name: "both, every setting of its own label",
			labels: map[string]string{
				"arcave.hook.pre-capture":           "on",
				"arcave.hook.pre-capture.user":      "postgres",
				"arcave.hook.pre-capture.timeout":   "2m",
				"arcave.hook.pre-capture.on-error":  "continue",
				"arcave.hook.post-capture":          "off",
				"arcave.hook.post-capture.user":     "1000:1000",
				"arcave.hook.post-capture.timeout":  "5s",
				"arcave.hook.post-capture.on-error": "fail",
			},
			wantPre: &model.Hook{
				Command: []string{"on"},
				User:    "postgres",
				Timeout: 2 * time.Minute,
				OnError: model.HookOnErrorContinue,
			},
			wantPost: &model.Hook{
				Command: []string{"off"},
				User:    "1000:1000",
				Timeout: 5 * time.Second,
				OnError: model.HookOnErrorFail,
			},
		},
		{
			name: "a shell command is split as a shell would, expanding nothing",
			labels: map[string]string{
				"arcave.hook.pre-capture": `sh -c 'psql -U "$POSTGRES_USER" -c CHECKPOINT; echo done'`,
			},
			wantPre: &model.Hook{
				Command: []string{"sh", "-c", `psql -U "$POSTGRES_USER" -c CHECKPOINT; echo done`},
				Timeout: time.Minute,
				OnError: model.HookOnErrorFail,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoProject(tt.labels, nil, nil, nil)

			// Act
			policy, err := Resolve(nopCtx(), introspection, hookDefaults)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantPre, policy.DecideService("app").PreCapture)
			assert.Equal(t, tt.wantPost, policy.DecideService("app").PostCapture)
			assert.Nil(t, policy.DecideService("db").PreCapture)
		})
	}
}

func TestResolve_RefusesHookLabels(t *testing.T) {
	tests := []struct {
		name    string
		labels  map[string]string
		wantMsg string
	}{
		{
			name:    "an empty command",
			labels:  map[string]string{"arcave.hook.pre-capture": "  "},
			wantMsg: `arcave.hook.pre-capture="  " on service app: want a command`,
		},
		{
			name:   "an unclosed quote",
			labels: map[string]string{"arcave.hook.pre-capture": "sh -c 'oops"},
			wantMsg: `arcave.hook.pre-capture="sh -c 'oops" on service app: unparsable command: ` +
				`invalid command line string`,
		},
		{
			name:   "a shell operator outside quotes",
			labels: map[string]string{"arcave.hook.post-capture": "sync && echo ok"},
			wantMsg: `arcave.hook.post-capture="sync && echo ok" on service app: a shell operator outside quotes: ` +
				`"&" at character 6; run the command through sh -c`,
		},
		{
			name:   "a redirect is named with its fd",
			labels: map[string]string{"arcave.hook.pre-capture": "redis-cli SAVE 2>/dev/null"},
			wantMsg: `arcave.hook.pre-capture="redis-cli SAVE 2>/dev/null" on service app: ` +
				`a shell operator outside quotes: "2>" at character 16; run the command through sh -c`,
		},
		{
			name:   "a timeout with no unit",
			labels: map[string]string{"arcave.hook.pre-capture": "true", "arcave.hook.pre-capture.timeout": "5"},
			wantMsg: `arcave.hook.pre-capture.timeout="5" on service app: want a positive Go duration, e.g. 30s: ` +
				`time: missing unit in duration "5"`,
		},
		{
			name:    "a zero timeout",
			labels:  map[string]string{"arcave.hook.pre-capture": "true", "arcave.hook.pre-capture.timeout": "0"},
			wantMsg: "want a positive Go duration, e.g. 30s: 0s is not positive",
		},
		{
			name:    "a negative timeout",
			labels:  map[string]string{"arcave.hook.post-capture": "true", "arcave.hook.post-capture.timeout": "-1s"},
			wantMsg: "want a positive Go duration, e.g. 30s: -1s is not positive",
		},
		{
			name:    "an unknown on-error",
			labels:  map[string]string{"arcave.hook.pre-capture": "true", "arcave.hook.pre-capture.on-error": "ignore"},
			wantMsg: `arcave.hook.pre-capture.on-error="ignore" on service app: want fail or continue`,
		},
		{
			name:    "a setting with no hook beside it",
			labels:  map[string]string{"arcave.hook.post-capture.user": "root"},
			wantMsg: `arcave.hook.post-capture.user="root" on service app: no arcave.hook.post-capture beside it`,
		},
		{
			name:    "a setting of the other hook",
			labels:  map[string]string{"arcave.hook.pre-capture": "true", "arcave.hook.post-capture.timeout": "1s"},
			wantMsg: "no arcave.hook.post-capture beside it",
		},
		{
			name:    "a typo is an unknown label",
			labels:  map[string]string{"arcave.hook.precapture": "true"},
			wantMsg: `arcave.hook.precapture="true" on service app: not a label arcave reads here`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoProject(tt.labels, nil, nil, nil)

			// Act
			_, err := Resolve(nopCtx(), introspection, hookDefaults)

			// Assert
			require.ErrorIs(t, err, model.ErrInvalidCapturePolicy)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// With every label ignored, no hook runs.
func TestResolveWithoutLabels_RunsNoHook(t *testing.T) {
	// Arrange
	introspection := demoProject(map[string]string{
		"arcave.hook.pre-capture":  "true",
		"arcave.hook.post-capture": "true",
	}, nil, nil, nil)

	// Act
	policy := ResolveWithoutLabels(introspection, hookDefaults)

	// Assert
	assert.Nil(t, policy.DecideService("app").PreCapture)
	assert.Nil(t, policy.DecideService("app").PostCapture)
}
