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
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// boolFlagValueHint is what pflag 1.1.0 appends to a bool flag's name in a listing.
const boolFlagValueHint = "[=true|false]"

// flagLine is one flag of a listing: the flag column, then the description past three spaces.
var flagLine = regexp.MustCompile(`^(\s+-.*?\S)(\s{3,})\S`)

// A bool flag is listed by its name alone, whatever pflag appends, and every listing keeps its
// descriptions in one column.
func TestUsage_BoolFlagsTakeNoValue(t *testing.T) {
	var commands []*cobra.Command
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		// What Execute adds before it parses: the listings the user sees carry both.
		cmd.InitDefaultHelpFlag()
		cmd.InitDefaultVersionFlag()
		commands = append(commands, cmd)
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)

	for _, cmd := range commands {
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			// Act
			usage := cmd.UsageString()

			// Assert
			assert.NotContains(t, usage, boolFlagValueHint)
			assert.Contains(t, usage, "-h, --help ")
			for _, block := range strings.Split(usage, "\n\n") {
				columns := map[int]bool{}
				narrowest := -1
				for _, line := range strings.Split(block, "\n") {
					if m := flagLine.FindStringSubmatchIndex(line); m != nil {
						columns[m[5]] = true
						if gap := m[5] - m[4]; narrowest < 0 || gap < narrowest {
							narrowest = gap
						}
					}
				}
				assert.LessOrEqual(t, len(columns), 1, "descriptions out of line:\n%s", block)
				if narrowest >= 0 {
					assert.Equal(t, 3, narrowest, "the longest flag sits three spaces from its description:\n%s", block)
				}
			}
		})
	}
	usage := toolsGenerateComposeUsage(t)
	assert.Contains(t, usage, "\n      --force            overwrite the output file if it exists\n")
}

// toolsGenerateComposeUsage is the usage of the one command with a bool flag of its own.
func toolsGenerateComposeUsage(t *testing.T) string {
	t.Helper()
	cmd, _, err := rootCmd.Find([]string{"tools", "generate-compose"})
	if err != nil {
		t.Fatal(err)
	}
	return cmd.UsageString()
}
