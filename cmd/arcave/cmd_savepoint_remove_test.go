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

package main

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/utils"
)

// --retain-recent 0 is a rule that retains none; only an absent flag is no rule.
func TestRetainRecentFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want *int
	}{
		{name: "an absent flag is no rule"},
		{name: "zero is a rule", args: []string{"--retain-recent", "0"}, want: utils.New(0)},
		{name: "a count is a rule", args: []string{"--retain-recent", "3"}, want: utils.New(3)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Cleanup(func() { savepointRemoveRetainRecent = 0 })
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			fs.IntVar(&savepointRemoveRetainRecent, "retain-recent", 0, "")
			require.NoError(t, fs.Parse(tt.args))

			// Act
			got := retainRecentFlag(fs)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
