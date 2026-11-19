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

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseHookOnError(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		want   HookOnError
		wantOK bool
	}{
		{name: "fail", token: "fail", want: HookOnErrorFail, wantOK: true},
		{name: "continue", token: "continue", want: HookOnErrorContinue, wantOK: true},
		{name: "an unknown token", token: "ignore"},
		{name: "case matters", token: "Fail"},
		{name: "empty", token: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, ok := ParseHookOnError(tt.token)

			// Assert
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
