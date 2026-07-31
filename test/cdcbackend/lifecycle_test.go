package cdcbackend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// I1: a capture writes restic's own layout and nothing else, which is what "open it
// with a stock restic CLI" rests on.
func TestSnapshotter_Snapshot_KeepsTheRepositoryLayout(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol, model.BackendTierSavepoint)

	// Act
	env.snapshot(t, backend, idFirst, nil)

	// Assert
	legal := []string{"keys/", "data/", "index/", "snapshots/", "locks/"}
	for _, key := range env.repoKeys(t, vol) {
		if key == configKey {
			continue
		}
		assert.Truef(t, hasAnyPrefix(key, legal), "%s is not part of a restic repository", key)
	}
}

// I2: two volumes are two repositories that share nothing, so one volume's
// exclusive operation cannot block another's.
func TestSnapshotter_Snapshot_KeepsRepositoriesPerVolume(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	first := env.newVolume(t, "db", "/var/lib/postgresql/data")
	second := env.newVolume(t, "app", "/srv/assets")

	// Act
	firstPayload := env.snapshot(t, env.backend(t, first, model.BackendTierSavepoint), idFirst, nil)
	secondPayload := env.snapshot(t, env.backend(t, second, model.BackendTierSavepoint), idFirst, nil)

	// Assert
	roots := []string{"restic/" + first.ObjectID + "/", "restic/" + second.ObjectID + "/"}
	for _, key := range objectNames(t, env.shared, "") {
		assert.Truef(t, hasAnyPrefix(key, roots), "%s lives outside either volume's repository", key)
	}
	assert.Equal(t, []string{firstPayload.SnapshotRef}, env.snapshotRefs(t, first))
	assert.Equal(t, []string{secondPayload.SnapshotRef}, env.snapshotRefs(t, second))
	assert.Contains(t, env.repoKeys(t, first), configKey)
	assert.Contains(t, env.repoKeys(t, second), configKey)
}

// hasAnyPrefix reports whether key starts with one of the prefixes.
func hasAnyPrefix(key string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
