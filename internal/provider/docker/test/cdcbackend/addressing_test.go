package cdcbackend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// A1: the volume is reached by its docker name, so the service's container is
// not needed at all. This is the case the whole change exists for: a user who
// ran `docker compose down` still has their data, and used to be unable to
// capture or restore it.
func TestSnapshotter_NoServiceContainer(t *testing.T) {
	// Arrange
	env := newEnv(t, wipeOptions())
	vol := env.newBareVolume(t, "db", "/var/lib/postgresql/data")
	backend := env.backend(t, vol)

	// Act
	payload := env.snapshot(t, backend, idFirst, nil)
	captured := env.hash(t, vol)
	env.mutate(t, vol)
	env.addFile(t, vol, "extra")
	require.NotEqual(t, captured, env.hash(t, vol), "the volume must actually diverge first")
	err := backend.Restore(env.ctx, dockermodel.BackendRestoreRequest{SnapshotID: idFirst, Payload: payload})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, captured, env.hash(t, vol))
	assert.Equal(t, []string{"/m/" + vol.ObjectID}, env.oracleSnapshots(t, vol)[0].Paths,
		"the snapshot holds the volume, not a path inside somebody's container")
}

// A2: a volume whose only mount exposes a subdirectory is captured and restored
// whole. The subpath is a property of that one container and lives only in the
// compose file — the arcaved does not even report it back — so a backend that
// went through the container would have snapshotted a fraction of the volume
// and, worse, restored a fraction over a wipe of all of it.
func TestSnapshotter_SubpathMountedVolume(t *testing.T) {
	// Arrange: "nested" is a subdirectory the fill creates; "blob" and "small"
	// sit outside it and no container ever sees them.
	env := newEnv(t, wipeOptions())
	vol := env.newSubpathVolume(t, "db", "/var/lib/postgresql/data", "nested")
	backend := env.backend(t, vol)

	payload := env.snapshot(t, backend, idFirst, nil)
	captured := env.hash(t, vol)
	require.Contains(t, captured, "/data/blob", "the volume must hold a file outside the subpath")

	env.mutate(t, vol)           // changes /data/blob, outside the subpath
	env.addFile(t, vol, "extra") // and a file the snapshot never carried
	require.NotEqual(t, captured, env.hash(t, vol))

	// Act
	err := backend.Restore(env.ctx, dockermodel.BackendRestoreRequest{SnapshotID: idFirst, Payload: payload})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, captured, env.hash(t, vol),
		"the whole volume comes back, including what the subpath mount never showed")
	assert.NotContains(t, env.hash(t, vol), "/data/extra", "the restore takes the added file away")

	// The snapshot names the volume's root, with no subpath segment anywhere in it.
	paths := env.oracleSnapshots(t, vol)[0].Paths
	assert.Equal(t, []string{"/m/" + vol.ObjectID}, paths)
	for _, p := range paths {
		assert.False(t, strings.HasSuffix(p, "/nested"), "the subpath must not narrow the capture")
	}
}
