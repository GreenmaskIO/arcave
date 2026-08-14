package cdc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A payload is written as a struct and read back as whatever the record's JSON
// round trip left behind, so both shapes have to decode alike.
func TestDecodePayload(t *testing.T) {
	want := Payload{SnapshotObjectID: "3f8a1c0d9b", DataAdded: 1048576, TotalBytesProcessed: 10485760}

	tests := []struct {
		name    string
		in      any
		want    Payload
		wantErr bool
	}{
		{name: "the struct the capture reported", in: want, want: want},
		{
			name: "the map a stored record decodes to",
			in: map[string]any{
				"restic_snapshot_id":    "3f8a1c0d9b",
				"data_added":            float64(1048576),
				"total_bytes_processed": float64(10485760),
			},
			want: want,
		},
		{
			name: "a map missing the counters still yields the id",
			in:   map[string]any{"restic_snapshot_id": "3f8a1c0d9b"},
			want: Payload{SnapshotObjectID: "3f8a1c0d9b"},
		},
		{name: "no payload at all", in: nil, wantErr: true},
		{name: "a payload of another backend", in: map[string]any{"size_bytes": float64(42)}},
		{name: "a payload that is not an object", in: "sp-payload", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := decodePayload(tt.in)

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
