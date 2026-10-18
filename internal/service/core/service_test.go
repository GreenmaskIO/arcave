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

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFanOutLimit(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		want        int
	}{
		{name: "a configured concurrency is the limit", concurrency: 5, want: 5},
		{name: "one runs the fan-out serially", concurrency: 1, want: 1},
		{name: "zero leaves the fan-out unbounded", concurrency: 0, want: -1},
		{name: "a negative concurrency leaves it unbounded", concurrency: -3, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := fanOutLimit(tt.concurrency)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
