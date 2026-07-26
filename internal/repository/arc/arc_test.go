package arc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The catalog is the built-in one plus, optionally, a user file merged over it.
// Each case writes its user file into t.TempDir() so the merge runs against real
// bytes and a real read error is reachable.
func TestRepository_List(t *testing.T) {
	tests := []struct {
		name     string
		userYAML string // written to a temp file; "" means no user file at all
		userFile string // an explicit path, for the cases that need a missing one
		wantErr  string
		verify   func(t *testing.T, got []model.ARCEntry)
	}{
		{
			name: "built-in catalog alone",
			verify: func(t *testing.T, got []model.ARCEntry) {
				pg := find(t, got, "postgres")
				assert.Equal(t, []string{"/var/lib/postgresql/data"}, pg.Volumes)
				assert.Contains(t, pg.Aliases, "pgvector/pgvector")
			},
		},
		{
			name: "a new image is appended after the built-ins",
			// The image must be one the built-in catalog does not carry, or the
			// merge would union it in place and this would assert nothing about
			// appending — and it must not be whichever entry happens to sit last
			// in default.yaml, or adding a built-in would break this case.
			userYAML: "- image: myorg/queue\n  volumes:\n    - /var/queue\n",
			verify: func(t *testing.T, got []model.ARCEntry) {
				require.NotEmpty(t, got)
				assert.Equal(t, model.ARCEntry{Image: "myorg/queue", Volumes: []string{"/var/queue"}}, got[len(got)-1])
			},
		},
		{
			name:     "a matching image is unioned in place",
			userYAML: "- image: postgres\n  aliases:\n    - pgvector/pgvector\n    - myorg/pg\n  volumes:\n    - /var/lib/postgresql/data\n    - /var/lib/postgresql/wal\n",
			verify: func(t *testing.T, got []model.ARCEntry) {
				pg := find(t, got, "postgres")
				// The built-in volume keeps its place and is not duplicated; the
				// user's addition lands after it.
				assert.Equal(t, []string{"/var/lib/postgresql/data", "/var/lib/postgresql/wal"}, pg.Volumes)
				assert.Equal(t, []string{"pgvector/pgvector", "postgis/postgis", "timescale/timescaledb", "myorg/pg"}, pg.Aliases)
			},
		},
		{
			name:     "an empty user file changes nothing",
			userYAML: "\n",
			verify: func(t *testing.T, got []model.ARCEntry) {
				assert.Equal(t, "postgres", find(t, got, "postgres").Image)
			},
		},
		{
			name:     "an entry without an image names its file and index",
			userYAML: "- image: minio/minio\n  volumes:\n    - /data\n- volumes:\n    - /nowhere\n",
			wantErr:  "entry 1 has no image",
		},
		{
			name:     "an unknown key is refused",
			userYAML: "- image: minio/minio\n  volume:\n    - /data\n",
			wantErr:  "parse recommender catalog",
		},
		{
			name:     "a missing user file is an error",
			userFile: filepath.Join(t.TempDir(), "absent.yaml"),
			wantErr:  "read recommender catalog",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var userFiles []string
			userFile := tt.userFile
			if tt.userYAML != "" {
				userFile = filepath.Join(t.TempDir(), "arc.yaml")
				require.NoError(t, os.WriteFile(userFile, []byte(tt.userYAML), 0o600))
			}
			if userFile != "" {
				userFiles = []string{userFile}
			}

			// Act
			got, err := NewARC(userFiles...).List()

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				if userFile != "" {
					assert.Contains(t, err.Error(), userFile)
				}
				return
			}
			require.NoError(t, err)
			tt.verify(t, got)
		})
	}
}

// Several user files fold left to right, each merged over the result of the
// last, so a personal catalog layers over a team one.
//
// Both images are ones the built-in catalog does not carry, so what is asserted
// here is the file-to-file merge alone — naming a built-in would union its
// volumes in too, and the case would then break whenever that entry changed.
func TestRepository_List_SeveralUserFiles(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	team := filepath.Join(dir, "team.yaml")
	require.NoError(t, os.WriteFile(team, []byte("- image: myorg/store\n  volumes:\n    - /data\n"), 0o600))
	personal := filepath.Join(dir, "personal.yaml")
	require.NoError(t, os.WriteFile(personal, []byte(
		"- image: myorg/store\n  volumes:\n    - /data\n    - /config\n"+
			"- image: myorg/queue\n  volumes:\n    - /var/queue\n"), 0o600))

	// Act
	got, err := NewARC(team, personal).List()

	// Assert
	require.NoError(t, err)
	// The team file's volume keeps its place and is not duplicated by the
	// personal file that repeats it; the personal addition follows.
	assert.Equal(t, []string{"/data", "/config"}, find(t, got, "myorg/store").Volumes)
	assert.Equal(t, []string{"/var/queue"}, find(t, got, "myorg/queue").Volumes)
	// Order across files: the earlier file's new image precedes the later one's.
	assert.Equal(t, model.ARCEntry{Image: "myorg/queue", Volumes: []string{"/var/queue"}}, got[len(got)-1])
}

// Every built-in entry must be usable: an entry with no volumes could never
// produce a recommendation, and a duplicate image key would make the lookup index
// order-dependent.
func TestRepository_List_BuiltIn(t *testing.T) {
	// Act
	got, err := NewARC().List()

	// Assert
	require.NoError(t, err)
	require.NotEmpty(t, got)
	seen := map[string]string{}
	for _, e := range got {
		assert.NotEmpty(t, e.Volumes, "entry %q declares no volumes", e.Image)
		for _, key := range append([]string{e.Image}, e.Aliases...) {
			owner, dup := seen[key]
			assert.False(t, dup, "image key %q is claimed by both %q and %q", key, owner, e.Image)
			seen[key] = e.Image
		}
	}
}

// find returns the entry for image, failing the test when the catalog has none.
func find(t *testing.T, entries []model.ARCEntry, image string) model.ARCEntry {
	t.Helper()
	for _, e := range entries {
		if e.Image == image {
			return e
		}
	}
	t.Fatalf("catalog has no entry for %q", image)
	return model.ARCEntry{}
}
