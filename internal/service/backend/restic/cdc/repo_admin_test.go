package cdc

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every question about a repository's state is an object question, answered
// without a container and without the repository password.

func TestRepoAdmin_HasConfig(t *testing.T) {
	tests := []struct {
		name string
		// plant is what the repository already holds.
		plant map[string]string
		want  bool
	}{
		{name: "an absent repository has never been initialised"},
		{
			name:  "a config object is the mark of an initialised one",
			plant: map[string]string{"config": "restic"},
			want:  true,
		},
		{
			name:  "objects without a config do not count",
			plant: map[string]string{"data/00/abc": "pack"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			admin := repoAdmin{store: newStore(t)}
			plant(t, admin, tt.plant)

			// Act
			got, err := admin.HasConfig(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A lock written inside a container carries that container's hostname, so
// `restic unlock` from the next one can never clear it. arcave holds the project
// flock for the whole workflow and the repository has no other client, so every
// lock standing at the start of an operation is an orphan by construction.
func TestRepoAdmin_ClearLocks(t *testing.T) {
	tests := []struct {
		name  string
		plant map[string]string
		// wantKept is the whole tree afterwards.
		wantKept []string
	}{
		{
			name:     "an unlocked repository is left alone",
			plant:    map[string]string{"config": "restic"},
			wantKept: []string{"config"},
		},
		{
			name: "every standing lock goes, and nothing else does",
			plant: map[string]string{
				"config":         "restic",
				"locks/abc123":   "orphan",
				"locks/def456":   "orphan",
				"snapshots/ff00": "a capture",
			},
			wantKept: []string{"config", "snapshots/ff00"},
		},
		{
			name:     "a repository that does not exist yet has no locks to clear",
			wantKept: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)
			admin := repoAdmin{store: store}
			plant(t, admin, tt.plant)

			// Act
			err := admin.ClearLocks(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Empty(t, objectsUnder(t, store, locksPrefix))
			assert.ElementsMatch(t, tt.wantKept, objectsUnder(t, store, ""))
		})
	}
}

// Dropping a snapshot object is byte for byte what `restic forget <id>` does on
// the wire, without the container and without the exclusive lock.
func TestRepoAdmin_DeleteSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		want    []string
		wantErr bool
	}{
		{name: "the named snapshot goes", ref: "3f8a", want: []string{"0011"}},
		{name: "one already gone is not an error", ref: "deadbeef", want: []string{"0011", "3f8a"}},
		{
			name:    "a ref that is not hex is refused before the delete",
			ref:     "../config",
			want:    []string{"0011", "3f8a"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)
			admin := repoAdmin{store: store}
			plant(t, admin, map[string]string{"snapshots/3f8a": "one", "snapshots/0011": "two"})

			// Act
			err := admin.DeleteSnapshot(context.Background(), tt.ref)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidSnapshotRef)
			} else {
				require.NoError(t, err)
			}
			assert.ElementsMatch(t, tt.want, objectsUnder(t, store, snapshotsPath))
		})
	}
}

// plant writes the objects a repository is supposed to already hold.
func plant(t *testing.T, admin repoAdmin, objects map[string]string) {
	t.Helper()

	for key, body := range objects {
		require.NoError(t, admin.store.PutObject(context.Background(), key, strings.NewReader(body)))
	}
}
