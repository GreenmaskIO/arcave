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

package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSize(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{size: 999, want: "999B"},
		{size: 1000, want: "1.0KB"},
		{size: 420_000, want: "420KB"},
		{size: 999_499, want: "999KB"},
		{size: 999_500, want: "1.0MB"},
		{size: 9_949_999, want: "9.9MB"},
		{size: 9_950_000, want: "10MB"},
		{size: 24_000_000, want: "24MB"},
		{size: 2_000_000_000_000_000_000, want: "2000PB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, Size(tt.size))
		})
	}
}
