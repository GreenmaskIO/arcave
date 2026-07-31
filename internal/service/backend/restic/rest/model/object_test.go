package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validID is a name of the shape restic writes: 64 lowercase hex characters.
var validID = strings.Repeat("0a1b2c3d", 8)

func TestParseObjectType(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ObjectType
		wantErr bool
	}{
		{name: "data", in: "data", want: TypeData},
		{name: "keys", in: "keys", want: TypeKeys},
		{name: "locks", in: "locks", want: TypeLocks},
		{name: "snapshots", in: "snapshots", want: TypeSnapshots},
		{name: "index", in: "index", want: TypeIndex},
		// config is addressed at its own path, never as a namespace.
		{name: "config is not a namespace", in: "config", wantErr: true},
		{name: "a namespace that does not exist", in: "packs", wantErr: true},
		{name: "the empty segment", in: "", wantErr: true},
		{name: "upper case", in: "DATA", wantErr: true},
		{name: "a traversal", in: "../data", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseObjectType(tt.in)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidObjectType)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseObjectID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "64 lowercase hex", in: validID},
		{name: "all zeroes", in: strings.Repeat("0", 64)},
		{name: "all f", in: strings.Repeat("f", 64)},
		{name: "63 characters", in: validID[:63], wantErr: true},
		{name: "65 characters", in: validID + "0", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "upper case", in: strings.ToUpper(validID), wantErr: true},
		{name: "not hex", in: strings.Repeat("g", 64), wantErr: true},
		{name: "a traversal of the right length", in: strings.Repeat("../", 21) + "a", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseObjectID(tt.in)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidObjectID)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, ObjectID(tt.in), got)
		})
	}
}
