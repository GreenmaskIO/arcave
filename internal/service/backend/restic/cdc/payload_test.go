package cdc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A payload is written as a struct and read back as whatever the record's JSON
// round trip left behind, so both shapes have to decode alike.
func TestDecodePayload(t *testing.T) {
	want := Payload{SnapshotRef: "3f8a1c0d9b", DataAdded: 1048576, TotalBytesProcessed: 10485760}

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
			name: "a map missing the counters still yields the ref",
			in:   map[string]any{"restic_snapshot_id": "3f8a1c0d9b"},
			want: Payload{SnapshotRef: "3f8a1c0d9b"},
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

// The ref is rendered into a storage key and into a container's command line,
// and it arrives from a record on disk — so it is gated by what it must be, not
// by a list of what it must not.
func TestValidateSnapshotRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{name: "a restic id", ref: "3f8a1c0d9b7e"},
		{name: "digits only", ref: "0011"},
		{name: "empty", ref: "", wantErr: true},
		{name: "a parent traversal", ref: "../../config", wantErr: true},
		{name: "a path separator", ref: "ab/cd", wantErr: true},
		{name: "uppercase hex", ref: "3F8A", wantErr: true},
		{name: "not hex at all", ref: "latest", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := validateSnapshotRef(tt.ref)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidSnapshotRef)
				return
			}
			assert.NoError(t, err)
		})
	}
}
