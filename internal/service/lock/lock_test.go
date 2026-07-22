package lock

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The whole point of the lock: the second holder must fail fast with the shared
// sentinel rather than block, so a user who starts a rollback while a create is
// running is told so instead of waiting on it.
func TestAcquire_SecondHolderFailsFast(t *testing.T) {
	// Arrange
	root := t.TempDir()
	release, err := New(root).Acquire(context.Background())
	require.NoError(t, err)

	// Act
	_, secondErr := New(root).Acquire(context.Background())

	// Assert
	require.ErrorIs(t, secondErr, model.ErrProjectLocked)
	assert.FileExists(t, filepath.Join(root, ".arcave", "lock"))
	require.NoError(t, release())
}

// Releasing drops the lock, so the next command can take it.
func TestAcquire_ReleaseAllowsReacquire(t *testing.T) {
	// Arrange
	root := t.TempDir()
	release, err := New(root).Acquire(context.Background())
	require.NoError(t, err)
	require.NoError(t, release())

	// Act
	again, err := New(root).Acquire(context.Background())

	// Assert
	require.NoError(t, err)
	require.NoError(t, again())
}

// The lock lives under .arcave, which a fresh project may not have yet — `arcave init`
// creates the config, but a savepoint command can run in a project whose .arcave was
// removed by hand.
func TestAcquire_CreatesTheLockDirectory(t *testing.T) {
	// Arrange
	root := t.TempDir()

	// Act
	release, err := New(root).Acquire(context.Background())

	// Assert
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, ".arcave", "lock"))
	require.NoError(t, release())
}

// Two projects lock independently: a rollback in one must never block the other.
func TestAcquire_IsPerProject(t *testing.T) {
	// Arrange
	first, second := t.TempDir(), t.TempDir()
	releaseFirst, err := New(first).Acquire(context.Background())
	require.NoError(t, err)

	// Act
	releaseSecond, err := New(second).Acquire(context.Background())

	// Assert
	require.NoError(t, err)
	require.NoError(t, releaseFirst())
	require.NoError(t, releaseSecond())
}
