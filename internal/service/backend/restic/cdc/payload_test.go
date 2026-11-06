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

package cdc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A payload is written as a struct and read back as whatever the record's JSON
// round trip left behind, so both shapes have to decode alike.
func TestDecodePayload(t *testing.T) {
	want := Payload{SnapshotObjectID: "3f8a1c0d9b"}

	tests := []struct {
		name    string
		in      any
		want    Payload
		wantErr bool
	}{
		{name: "the struct the capture reported", in: want, want: want},
		{
			name: "the map a stored record decodes to",
			in:   map[string]any{"restic_snapshot_id": "3f8a1c0d9b"},
			want: want,
		},
		{name: "no payload at all", in: nil, wantErr: true},
		{name: "a payload of another backend", in: map[string]any{"size_bytes": float64(42)}},
		{name: "a payload that is not an object", in: "sp-payload", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := DecodePayload(tt.in)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
