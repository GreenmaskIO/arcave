package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ValidSavepointID is the first path-safety gate: an id that is not a run of
// digits must never become a storage key.
func TestValidSavepointID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		ok   bool
	}{
		{name: "unix seconds", id: "1780335122", ok: true},
		{name: "empty", id: ""},
		{name: "current directory", id: "."},
		{name: "parent traversal", id: "../.."},
		{name: "path with a traversal", id: "../x.json"},
		{name: "absolute path", id: "/root"},
		{name: "a name, not an id", id: "before-migration"},
		{name: "digits with a separator", id: "17803/35122"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := ValidSavepointID(tt.id)

			// Assert
			if tt.ok {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}

func TestNamespace_String(t *testing.T) {
	tests := []struct {
		name string
		ns   Namespace
		want string
	}{
		{name: "a savepoint is addressed by its bare id", ns: SavepointNamespace("100"), want: "100"},
		{name: "a safety backup carries the rollback prefix", ns: SafetyBackupNamespace("100"), want: "rollback-100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.ns.String())
		})
	}
}

func TestNamespace_Validate(t *testing.T) {
	tests := []struct {
		name string
		ns   Namespace
		ok   bool
	}{
		{name: "savepoint", ns: SavepointNamespace("100"), ok: true},
		{name: "safety backup", ns: SafetyBackupNamespace("100"), ok: true},
		{name: "unknown kind", ns: Namespace{Kind: "volume", ID: "100"}},
		{name: "valid kind, injected id", ns: SavepointNamespace("../..")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.ns.Validate()

			// Assert
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}
