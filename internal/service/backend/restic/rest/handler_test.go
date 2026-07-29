package rest

import (
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
			expect: func(repo *repositoryMock) { repo.On("Create", mock.Anything).Return(nil).Once() },
		},
		{
			name: "stat the config", method: http.MethodHead, target: "/config",
			expect: func(repo *repositoryMock) { repo.On("StatConfig", mock.Anything).Return(int64(12), nil).Once() },
		},
		{
			name: "read the config", method: http.MethodGet, target: "/config",
			expect: func(repo *repositoryMock) {
				repo.On("OpenConfig", mock.Anything).Return(io.NopCloser(strings.NewReader("[repository]")), nil).Once()
			},
		},
		{
			name: "write the config", method: http.MethodPost, target: "/config", body: "[repository]",
			expect: func(repo *repositoryMock) {
				repo.On("SaveConfig", mock.Anything, mock.Anything).Return(nil).Once()
			},
		},
		{
			name: "delete the config", method: http.MethodDelete, target: "/config",
			expect: func(repo *repositoryMock) { repo.On("DeleteConfig", mock.Anything).Return(nil).Once() },
		},
		{
			name: "list a type", method: http.MethodGet, target: "/data/",
			expect: func(repo *repositoryMock) {
				repo.On("ListBlobs", mock.Anything, "data").Return([]blob{{Name: blobID, Size: 3}}, nil).Once()
			},
		},
		{
			name: "stat a blob", method: http.MethodHead, target: "/index/" + blobID,
			expect: func(repo *repositoryMock) {
				repo.On("StatBlob", mock.Anything, "index", blobID).Return(int64(3), nil).Once()
			},
		},
		{
			name: "read a blob", method: http.MethodGet, target: "/locks/" + blobID,
			expect: func(repo *repositoryMock) {
				repo.On("OpenBlob", mock.Anything, "locks", blobID).Return(newReadSeekCloser("payload"), nil).Once()
			},
		},
		{
			name: "write a blob", method: http.MethodPost, target: "/keys/" + blobID, body: "key-bytes",
			expect: func(repo *repositoryMock) {
				repo.On("SaveBlob", mock.Anything, "keys", blobID, mock.Anything).Return(nil).Once()
			},
		},
		{
			name: "delete a blob", method: http.MethodDelete, target: "/snapshots/" + blobID,
			expect: func(repo *repositoryMock) {
				repo.On("DeleteBlob", mock.Anything, "snapshots", blobID).Return(nil).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, repo := newMockedHandler()
			tt.expect(repo)

			// Act
			rec := do(t, h, tt.method, tt.target, tt.body)

			// Assert
			assert.Equal(t, http.StatusOK, rec.Code)
			repo.AssertExpectations(t)
		})
	}
}

// An upload reaches the repository as the request body itself, unread: nothing
// on that path needs to see the bytes, and buffering them would be the one
// thing a stream of pack files must never do.
func TestHandler_saveBlob_HandsOverTheRequestBody(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler()
	var got string
	repo.On("SaveBlob", mock.Anything, "data", blobID, mock.Anything).
		Run(func(args mock.Arguments) {
			body, err := io.ReadAll(args.Get(3).(io.Reader))
			require.NoError(t, err)
			got = string(body)
		}).
		Return(nil).Once()

	// Act
	rec := do(t, h, http.MethodPost, "/data/"+blobID, "pack-bytes")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pack-bytes", got)
	repo.AssertExpectations(t)
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
					Return(int64(0), fmt.Errorf("%w: data/%s", errObjectNotFound, blobID)).Once()
			},
		},
		{
			name: "a missing config is a 404", method: http.MethodGet, target: "/config", want: http.StatusNotFound,
			expect: func(repo *repositoryMock) {
				repo.On("OpenConfig", mock.Anything).Return(nil, fmt.Errorf("%w: config", errObjectNotFound)).Once()
			},
		},
		{
			name: "a blob that cannot be opened is a 404", method: http.MethodGet, target: "/data/" + blobID, want: http.StatusNotFound,
			expect: func(repo *repositoryMock) {
				repo.On("OpenBlob", mock.Anything, "data", blobID).Return(nil, errObjectNotFound).Once()
			},
		},
		{
			name: "an existing config is a 403", method: http.MethodPost, target: "/config", want: http.StatusForbidden,
			expect: func(repo *repositoryMock) {
				repo.On("SaveConfig", mock.Anything, mock.Anything).Return(errConfigExists).Once()
			},
		},
		{
			name: "a failed init is a 500", method: http.MethodPost, target: "/?create=true", want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) { repo.On("Create", mock.Anything).Return(failed).Once() },
		},
		{
			name: "a failed listing is a 500", method: http.MethodGet, target: "/data/", want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) { repo.On("ListBlobs", mock.Anything, "data").Return(nil, failed).Once() },
		},
		{
			name: "a failed upload is a 500", method: http.MethodPost, target: "/data/" + blobID, want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) {
				repo.On("SaveBlob", mock.Anything, "data", blobID, mock.Anything).Return(failed).Once()
			},
		},
		{
			name: "a failed delete is a 500", method: http.MethodDelete, target: "/data/" + blobID, want: http.StatusInternalServerError,
			expect: func(repo *repositoryMock) {
				repo.On("DeleteBlob", mock.Anything, "data", blobID).Return(failed).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, repo := newMockedHandler()
			tt.expect(repo)

			// Act
			rec := do(t, h, tt.method, tt.target, "")

			// Assert
			assert.Equal(t, tt.want, rec.Code)
			repo.AssertExpectations(t)
		})
	}
}

// A 500 says only that: the cause goes to the log, never to the body. A restic
// client can do nothing with it, and it names the store's layout.
func TestHandler_ServeHTTP_DoesNotLeakTheCause(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler()
	repo.On("ListBlobs", mock.Anything, "data").
		Return(nil, fmt.Errorf("listing data: open /srv/backups/tenant-42/arcave: permission denied")).Once()

	// Act
	rec := do(t, h, http.MethodGet, "/data/", "")

	// Assert
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Internal Server Error\n", rec.Body.String())
	repo.AssertExpectations(t)
}
