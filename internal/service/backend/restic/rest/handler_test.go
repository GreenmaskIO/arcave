package rest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The handler over a double, with no store behind it at all: these are the
// tests of the one job it has — decide what a request means, call the
// repository once with what the URL named, and turn what comes back into a
// status. What the repository then does with the objects is service_test.go's,
// and that the two together answer like rest-server is oracle_test.go's.

// newMockedHandler returns the handler over a fresh double, plus the double.
func newMockedHandler(t *testing.T) (http.Handler, *repositoryMock) {
	t.Helper()
	repo := &repositoryMock{}
	t.Cleanup(func() { repo.AssertExpectations(t) })
	return newHandler(repo), repo
}

// Every route reaches exactly one repository method, carrying the type and name
// the URL named and nothing else. The expectation is set with the exact
// arguments, so AssertExpectations failing is the assertion: a handler that
// passed "/data" where "data" was meant, or swapped the two path values, has no
// matching call to satisfy.
func TestHandler_ServeHTTP_DelegatesToRepository(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		accept string
		expect func(repo *repositoryMock)
	}{
		{
			name: "init", method: http.MethodPost, target: "/?create=true",
			expect: func(repo *repositoryMock) { repo.On("Create", mock.Anything).Return(nil) },
		},
		{
			name: "stat the config", method: http.MethodHead, target: "/config",
			expect: func(repo *repositoryMock) { repo.On("StatConfig", mock.Anything).Return(int64(12), nil) },
		},
		{
			name: "read the config", method: http.MethodGet, target: "/config",
			expect: func(repo *repositoryMock) {
				repo.On("OpenConfig", mock.Anything).Return(io.NopCloser(strings.NewReader("[repository]")), nil)
			},
		},
		{
			name: "write the config", method: http.MethodPost, target: "/config", body: "[repository]",
			expect: func(repo *repositoryMock) {
				repo.On("SaveConfig", mock.Anything, mock.Anything).Return(nil)
			},
		},
		{
			name: "delete the config", method: http.MethodDelete, target: "/config",
			expect: func(repo *repositoryMock) { repo.On("DeleteConfig", mock.Anything).Return(nil) },
		},
		{
			name: "list a type", method: http.MethodGet, target: "/data/",
			expect: func(repo *repositoryMock) {
				repo.On("ListBlobs", mock.Anything, "data").Return([]blob{{Name: blobID, Size: 3}}, nil)
			},
		},
		{
			name: "stat a blob", method: http.MethodHead, target: "/index/" + blobID,
			expect: func(repo *repositoryMock) {
				repo.On("StatBlob", mock.Anything, "index", blobID).Return(int64(3), nil)
			},
		},
		{
			name: "read a blob", method: http.MethodGet, target: "/locks/" + blobID,
			expect: func(repo *repositoryMock) {
				repo.On("OpenBlob", mock.Anything, "locks", blobID).Return(newReadSeekCloser("payload"), nil)
			},
		},
		{
			name: "write a blob", method: http.MethodPost, target: "/keys/" + blobID, body: "key-bytes",
			expect: func(repo *repositoryMock) {
				repo.On("SaveBlob", mock.Anything, "keys", blobID, mock.Anything).Return(nil)
			},
		},
		{
			name: "delete a blob", method: http.MethodDelete, target: "/snapshots/" + blobID,
			expect: func(repo *repositoryMock) {
				repo.On("DeleteBlob", mock.Anything, "snapshots", blobID).Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, repo := newMockedHandler(t)
			tt.expect(repo)

			// Act
			rec := do(t, h, tt.method, tt.target, tt.body)

			// Assert
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// An upload reaches the repository as the request body itself, unread: nothing
// on that path needs to see the bytes, and buffering them would be the one
// thing a stream of pack files must never do.
func TestHandler_saveBlob_HandsOverTheRequestBody(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler(t)
	var got string
	repo.On("SaveBlob", mock.Anything, "data", blobID, mock.Anything).
		Run(func(args mock.Arguments) {
			body, err := io.ReadAll(args.Get(3).(io.Reader))
			require.NoError(t, err)
			got = string(body)
		}).
		Return(nil)

	// Act
	rec := do(t, h, http.MethodPost, "/data/"+blobID, "pack-bytes")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pack-bytes", got)
}

// The two conditions the repository names, and everything else, become the
// three statuses the protocol has for them. The sentinels are matched with
// errors.Is, so a wrapped one — which is what the repository actually returns —
// must map the same way.
func TestHandler_ServeHTTP_MapsRepositoryErrors(t *testing.T) {
	failed := fmt.Errorf("the store is unreachable at /srv/backups/tenant-42")

	tests := []struct {
		name   string
		method string
		target string
		expect func(repo *repositoryMock)
		want   int
	}{
		{
			name: "a missing blob is a 404", method: http.MethodHead, target: "/data/" + blobID, want: http.StatusNotFound,
			expect: func(repo *repositoryMock) {
				repo.On("StatBlob", mock.Anything, "data", blobID).
					Return(int64(0), fmt.Errorf("%w: data/%s", errObjectNotFound, blobID))
			},
		},
		{
			name: "a missing config is a 404", method: http.MethodGet, target: "/config", want: http.StatusNotFound,
			expect: func(repo *repositoryMock) {
				repo.On("OpenConfig", mock.Anything).Return(nil, fmt.Errorf("%w: config", errObjectNotFound))
			},
		},
		{
			name: "a blob that cannot be opened is a 404", method: http.MethodGet, target: "/data/" + blobID, want: http.StatusNotFound,
			expect: func(repo *repositoryMock) {
				repo.On("OpenBlob", mock.Anything, "data", blobID).Return(nil, errObjectNotFound)
			},
		},
		{
			name: "an existing config is a 403", method: http.MethodPost, target: "/config", want: http.StatusForbidden,
			expect: func(repo *repositoryMock) {
				repo.On("SaveConfig", mock.Anything, mock.Anything).Return(errConfigExists)
			},
		},
		{
			name: "a failed init is a 500", method: http.MethodPost, target: "/?create=true", want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) { repo.On("Create", mock.Anything).Return(failed) },
		},
		{
			name: "a failed listing is a 500", method: http.MethodGet, target: "/data/", want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) { repo.On("ListBlobs", mock.Anything, "data").Return(nil, failed) },
		},
		{
			name: "a failed upload is a 500", method: http.MethodPost, target: "/data/" + blobID, want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) {
				repo.On("SaveBlob", mock.Anything, "data", blobID, mock.Anything).Return(failed)
			},
		},
		{
			name: "a failed delete is a 500", method: http.MethodDelete, target: "/data/" + blobID, want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) {
				repo.On("DeleteBlob", mock.Anything, "data", blobID).Return(failed)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, repo := newMockedHandler(t)
			tt.expect(repo)

			// Act
			rec := do(t, h, tt.method, tt.target, "")

			// Assert
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// A 500 says only that: the cause goes to the log, never to the body. A restic
// client can do nothing with it, and it names the store's layout.
func TestHandler_ServeHTTP_DoesNotLeakTheCause(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler(t)
	repo.On("ListBlobs", mock.Anything, "data").
		Return(nil, fmt.Errorf("listing data: open /srv/backups/tenant-42/arcave: permission denied"))

	// Act
	rec := do(t, h, http.MethodGet, "/data/", "")

	// Assert
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Internal Server Error\n", rec.Body.String())
}

// repositoryMock is the handler's view of a repository, doubled. It is what
// makes the seam worth having: the handler is exercised without a store, a
// filesystem or a single real object.
type repositoryMock struct {
	mock.Mock
}

// compile-time assertion: the double must keep up with the interface.
var _ repository = (*repositoryMock)(nil)

func (m *repositoryMock) Create(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *repositoryMock) StatConfig(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *repositoryMock) OpenConfig(ctx context.Context) (io.ReadCloser, error) {
	args := m.Called(ctx)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *repositoryMock) SaveConfig(ctx context.Context, body io.Reader) error {
	return m.Called(ctx, body).Error(0)
}

func (m *repositoryMock) DeleteConfig(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *repositoryMock) ListBlobs(ctx context.Context, objectType string) ([]blob, error) {
	args := m.Called(ctx, objectType)
	blobs, _ := args.Get(0).([]blob)
	return blobs, args.Error(1)
}

func (m *repositoryMock) StatBlob(ctx context.Context, objectType, name string) (int64, error) {
	args := m.Called(ctx, objectType, name)
	return args.Get(0).(int64), args.Error(1)
}

func (m *repositoryMock) OpenBlob(ctx context.Context, objectType, name string) (io.ReadSeekCloser, error) {
	args := m.Called(ctx, objectType, name)
	f, _ := args.Get(0).(io.ReadSeekCloser)
	return f, args.Error(1)
}

func (m *repositoryMock) SaveBlob(ctx context.Context, objectType, name string, body io.Reader) error {
	return m.Called(ctx, objectType, name, body).Error(0)
}

func (m *repositoryMock) DeleteBlob(ctx context.Context, objectType, name string) error {
	return m.Called(ctx, objectType, name).Error(0)
}

// readSeekCloser is an in-memory stand-in for what OpenBlob returns, which
// http.ServeContent seeks over to measure the object.
type readSeekCloser struct {
	*strings.Reader
}

func newReadSeekCloser(payload string) io.ReadSeekCloser {
	return readSeekCloser{Reader: strings.NewReader(payload)}
}

func (readSeekCloser) Close() error { return nil }
