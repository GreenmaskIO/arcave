package registry

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// stubRepository is never asked anything — these tests are about which name
// serves which instance. The embedded nil interface says so: a call would panic.
type stubRepository struct{ model.Repository }

// testRepoName is the shape a caller registers under: the tier and the object id
// flattened into one segment.
const testRepoName = "savepoint-volume-pgdata"

func TestRegistry_Register(t *testing.T) {
	tests := []struct {
		name string
		// taken is registered first, for the duplicate case to collide with.
		taken     string
		register  string
		wantErrIs error
	}{
		{name: "a name of the shape a caller flattens", register: testRepoName},
		{name: "a name already serving a repository", taken: testRepoName, register: testRepoName, wantErrIs: model.ErrRepositoryNameRegistered},
		{name: "no name at all", register: "", wantErrIs: errInvalidName},
		{name: "a name of two segments", register: "safety/pgdata", wantErrIs: errInvalidName},
		{name: "a name with a leading slash", register: "/pgdata", wantErrIs: errInvalidName},
		// Shadowed by the documentation's route, so reachable by nobody.
		{name: "the documentation's own segment", register: "docs", wantErrIs: errInvalidName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			reg := New()
			if tt.taken != "" {
				require.NoError(t, reg.Register(tt.taken, &stubRepository{}))
			}

			// Act
			err := reg.Register(tt.register, &stubRepository{})

			// Assert
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
				return
			}
			require.NoError(t, err)
			_, err = reg.Lookup(tt.register)
			assert.NoError(t, err, "the name was accepted and resolves to nothing")
		})
	}
}

// TestRegistry_Lookup pins that a name resolves to its own repository — what a
// resolver always answering with the first entry would fail.
func TestRegistry_Lookup(t *testing.T) {
	// Arrange
	first, second := &stubRepository{}, &stubRepository{}
	reg := New()
	require.NoError(t, reg.Register("first", first))
	require.NoError(t, reg.Register("second", second))

	tests := []struct {
		name      string
		lookup    string
		want      model.Repository
		wantErrIs error
	}{
		{name: "the name registered first", lookup: "first", want: first},
		{name: "the name registered after it", lookup: "second", want: second},
		{name: "a name nothing was registered under", lookup: "third", wantErrIs: model.ErrRepositoryNotFound},
		{name: "no name at all", lookup: "", wantErrIs: model.ErrRepositoryNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := reg.Lookup(tt.lookup)

			// Assert
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Same(t, tt.want, got)
		})
	}
}

// TestRegistry_Lookup_AfterUnregister: a withdrawn name stops resolving at once.
func TestRegistry_Lookup_AfterUnregister(t *testing.T) {
	// Arrange
	reg := New()
	require.NoError(t, reg.Register(testRepoName, &stubRepository{}))

	// Act
	reg.Unregister(testRepoName)

	// Assert
	_, err := reg.Lookup(testRepoName)
	require.ErrorIs(t, err, model.ErrRepositoryNotFound)
}

// TestRegistry_Unregister_Idempotent: a withdrawal is deferred, so a name already
// gone must not become a failure of its own — and the name is free again.
func TestRegistry_Unregister_Idempotent(t *testing.T) {
	// Arrange
	reg := New()
	require.NoError(t, reg.Register(testRepoName, &stubRepository{}))

	// Act
	reg.Unregister(testRepoName)
	reg.Unregister(testRepoName)

	// Assert
	assert.NoError(t, reg.Register(testRepoName, &stubRepository{}))
}

// TestRegistry_Concurrent is the one test that says anything under -race: two
// volumes' operations overlapping on one server.
func TestRegistry_Concurrent(t *testing.T) {
	// Arrange
	const rounds = 200
	reg := New()
	names := []string{"savepoint-volume-a", "safety-volume-a"}
	for _, name := range names {
		require.NoError(t, reg.Register(name, &stubRepository{}))
	}

	// Act
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range rounds {
				_, _ = reg.Lookup(name)
			}
		}()
		go func() {
			defer wg.Done()
			for range rounds {
				reg.Unregister(name)
				_ = reg.Register(name, &stubRepository{})
			}
		}()
	}
	wg.Wait()

	// Assert
	reg.Unregister(names[0])
	_, err := reg.Lookup(names[0])
	assert.ErrorIs(t, err, model.ErrRepositoryNotFound)
}
