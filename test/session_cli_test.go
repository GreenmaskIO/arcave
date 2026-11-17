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

package test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestSession_CurrentSessionErrors_NameTheCLIFix(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	initArcave(t, env)
	feat := env.create(t, "feat")
	gone := model.NewSessionID()

	tests := []struct {
		name    string
		pointer string
		args    []string
		wantFix string
	}{
		{
			name:    "a dangling pointer",
			pointer: gone.String(),
			args:    []string{"savepoint", "list", "-p", "demo"},
			wantFix: "run 'arcave session unset' or 'arcave session set <session>'",
		},
		{
			name:    "an unreadable pointer",
			pointer: "feat",
			args:    []string{"savepoint", "list", "-p", "demo"},
			wantFix: "run 'arcave session unset' or 'arcave session set <session>'",
		},
		{
			name:    "removing the current session",
			pointer: feat.ID.String(),
			args:    []string{"session", "remove", "feat"},
			wantFix: "or pass --force to delete it and clear the pointer",
		},
	}
	for _, tt := range tests {
		// Not parallel: every case rewrites the one pointer.
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(env.pointerPath(), []byte(tt.pointer+"\n"), 0o644))

			// Act
			out, err := arcaveCommand(t, env, tt.args...).CombinedOutput()

			// Assert
			require.Error(t, err)
			assert.Contains(t, string(out), tt.wantFix)
		})
	}
	assert.DirExists(t, env.sessionDir(feat.ID), "a refused delete removes nothing")
}
