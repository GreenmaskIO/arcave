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

package utils

import (
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
)

func TestMeasureObjects(t *testing.T) {
	older := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		objects   []storages.ObjectStat
		wantBytes int64
		wantTime  time.Time
	}{
		{name: "none"},
		{
			name:      "the newest write",
			objects:   []storages.ObjectStat{{Size: 2, LastModified: newer}, {Size: 3, LastModified: older}},
			wantBytes: 5, wantTime: newer,
		},
		{
			name:      "one without a time makes the time unknown",
			objects:   []storages.ObjectStat{{Size: 2, LastModified: newer}, {Size: 3}},
			wantBytes: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			bytes, lastModified := MeasureObjects(tt.objects)

			// Assert
			assert.Equal(t, tt.wantBytes, bytes)
			assert.Equal(t, tt.wantTime, lastModified)
		})
	}
}
