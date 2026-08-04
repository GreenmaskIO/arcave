package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/api"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// The twelve operations run through the generated contract into a recorder, over
// a mock repository. A case that must ask the repository nothing declares nothing.

// errStore is a store failure that is neither sentinel — no operation declares an
// answer for it, so it is the 500.
var errStore = errors.New("store unreachable")

// errNotFound is the sentinel as a repository raises it: wrapped, since an
// operation matches it with errors.Is and never by equality.
var errNotFound = fmt.Errorf("%w: %s", model.ErrObjectNotFound, testID)

// blobBody is the object every read test is served from; its length is what the
// Range table is expressed against.
var blobBody = []byte("0123456789abcdef")

// The name a repository is served under and one nothing is, both real arcave
// ObjectIDs: one path segment, the volume's identity rendered into it.
const (
	testRepoName    = "volume-named-pgdata"
	unknownRepoName = "volume-service-app-etc-cfg"
)

// serving is the resolver a case runs against: repo under testRepoName, nothing
// under any other name.
func serving(repo model.Repository) model.RepositoryResolver {
	return stubResolver{name: testRepoName, repo: repo}
}

func TestHandler_CreateRepository(t *testing.T) {
	tests := []struct {
		name       string
		create     string
		wantCreate bool
		wantStatus int
	}{
		{name: "an init", create: "true", wantCreate: true, wantStatus: http.StatusOK},
		{name: "create=false is not an init", create: "false", wantStatus: http.StatusBadRequest},
		{name: "an absent create is not an init", create: "", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			if tt.wantCreate {
				store.On("Create", mock.Anything).Return(nil).Once()
			}
			h := New(serving(store))
			request := api.CreateRepositoryRequestObject{ArcaveObjectID: testRepoName, Params: api.CreateRepositoryParams{Create: tt.create}}

			// Act
			resp, err := h.CreateRepository(context.Background(), request)

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitCreateRepositoryResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			store.AssertExpectations(t)
		})
	}
}

// TestHandler_CreateRepository_AlreadyInitialised pins that an init over an
// existing repository is a success, not the 403 — that one belongs to SaveConfig.
func TestHandler_CreateRepository_AlreadyInitialised(t *testing.T) {
	// Arrange
	store := &mockRepository{}
	store.On("Create", mock.Anything).Return(model.ErrConfigExists).Once()
	h := New(serving(store))
	request := api.CreateRepositoryRequestObject{ArcaveObjectID: testRepoName, Params: api.CreateRepositoryParams{Create: "true"}}

	// Act
	resp, err := h.CreateRepository(context.Background(), request)

	// Assert
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	require.NoError(t, resp.VisitCreateRepositoryResponse(rec))
	assert.Equal(t, http.StatusOK, rec.Code)
	store.AssertExpectations(t)
}

// TestHandler_CreateRepository_StoreFailure pins that a repository failure is
// returned rather than rendered: an undeclared condition is not this call's to answer.
func TestHandler_CreateRepository_StoreFailure(t *testing.T) {
	// Arrange
	store := &mockRepository{}
	store.On("Create", mock.Anything).Return(errStore).Once()
	h := New(serving(store))

	// Act
	resp, err := h.CreateRepository(context.Background(), api.CreateRepositoryRequestObject{
		ArcaveObjectID: testRepoName,
		Params:         api.CreateRepositoryParams{Create: "true"},
	})

	// Assert
	require.ErrorIs(t, err, errStore)
	assert.Nil(t, resp)
	store.AssertExpectations(t)
}

// TestHandler_DeleteRepository pins the one operation arcave refuses: a repository's
// lifetime is the savepoint layer's. The empty mock is half the assertion.
func TestHandler_DeleteRepository(t *testing.T) {
	// Arrange
	store := &mockRepository{}
	h := New(serving(store))

	// Act
	resp, err := h.DeleteRepository(context.Background(), api.DeleteRepositoryRequestObject{ArcaveObjectID: testRepoName})

	// Assert
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	require.NoError(t, resp.VisitDeleteRepositoryResponse(rec))
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	store.AssertExpectations(t)
}

func TestHandler_StatConfig(t *testing.T) {
	tests := []struct {
		name       string
		size       int64
		err        error
		wantStatus int
		wantLength string
	}{
		{name: "a repository that is initialised", size: 16, wantStatus: http.StatusOK, wantLength: "16"},
		{name: "a repository that is not", err: errNotFound, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			store.On("StatConfig", mock.Anything).Return(tt.size, tt.err).Once()
			h := New(serving(store))

			// Act
			resp, err := h.StatConfig(context.Background(), api.StatConfigRequestObject{ArcaveObjectID: testRepoName})

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitStatConfigResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantLength, rec.Header().Get("Content-Length"))
			assert.Empty(t, rec.Body.String())
			store.AssertExpectations(t)
		})
	}
}

func TestHandler_GetConfig(t *testing.T) {
	t.Run("the config", func(t *testing.T) {
		// Arrange
		body := newStream(string(blobBody))
		store := &mockRepository{}
		store.On("OpenConfig", mock.Anything).Return(body, nil).Once()
		h := New(serving(store))

		// Act
		resp, err := h.GetConfig(context.Background(), api.GetConfigRequestObject{ArcaveObjectID: testRepoName})

		// Assert
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		require.NoError(t, resp.VisitGetConfigResponse(rec))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
		assert.Equal(t, string(blobBody), rec.Body.String())
		assert.True(t, body.closed, "the config stream was not given back")
		store.AssertExpectations(t)
	})

	t.Run("no config", func(t *testing.T) {
		// Arrange
		store := &mockRepository{}
		store.On("OpenConfig", mock.Anything).Return(nil, errNotFound).Once()
		h := New(serving(store))

		// Act
		resp, err := h.GetConfig(context.Background(), api.GetConfigRequestObject{ArcaveObjectID: testRepoName})

		// Assert
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		require.NoError(t, resp.VisitGetConfigResponse(rec))
		assert.Equal(t, http.StatusNotFound, rec.Code)
		store.AssertExpectations(t)
	})
}

// TestHandler_SaveConfig pins the two answers of an init's write, and that the
// body is handed over as it arrived — nothing on this path reads the bytes.
func TestHandler_SaveConfig(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "init writes it", wantStatus: http.StatusOK},
		// The refusal restic's init depends on: never re-initialise a repository holding data.
		{name: "a second init is refused", err: model.ErrConfigExists, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			body := strings.NewReader("written")
			store := &mockRepository{}
			store.On("SaveConfig", mock.Anything, body).Return(tt.err).Once()
			h := New(serving(store))

			// Act
			resp, err := h.SaveConfig(context.Background(), api.SaveConfigRequestObject{ArcaveObjectID: testRepoName, Body: body})

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitSaveConfigResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, len("written"), body.Len(), "the body was read before the repository saw it")
			store.AssertExpectations(t)
		})
	}
}

func TestHandler_DeleteConfig(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "a config that is there"},
		// Idempotent: restic retries deletes, so one already gone is a success.
		{name: "a config that is already gone", err: errNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			store.On("DeleteConfig", mock.Anything).Return(tt.err).Once()
			h := New(serving(store))

			// Act
			resp, err := h.DeleteConfig(context.Background(), api.DeleteConfigRequestObject{ArcaveObjectID: testRepoName})

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitDeleteConfigResponse(rec))
			assert.Equal(t, http.StatusOK, rec.Code)
			store.AssertExpectations(t)
		})
	}
}

// TestHandler_ListBlobs pins the one shape served — name and size per object, under
// restic's v2 media type — and the domain type reaching the repository.
func TestHandler_ListBlobs(t *testing.T) {
	// Arrange
	otherID := model.ObjectID(strings.Repeat("f", 64))
	store := &mockRepository{}
	store.On("ListBlobs", mock.Anything, model.ListBlobsRequest{Type: model.TypeData}).Return([]model.Blob{
		{Name: string(testID), Size: 16},
		{Name: string(otherID), Size: 3},
	}, nil).Once()
	h := New(serving(store))

	// Act
	resp, err := h.ListBlobs(context.Background(), api.ListBlobsRequestObject{ArcaveObjectID: testRepoName, Type: api.Data})

	// Assert
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	require.NoError(t, resp.VisitListBlobsResponse(rec))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/vnd.x.restic.rest.v2", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `[{"name":"`+string(testID)+`","size":16},`+
		`{"name":"`+string(otherID)+`","size":3}]`, rec.Body.String())
	store.AssertExpectations(t)
}

// TestHandler_ListBlobs_Empty pins that an empty namespace renders as [] and not
// null: restic lists a fresh repository, and the repository answers a nil slice.
func TestHandler_ListBlobs_Empty(t *testing.T) {
	// Arrange
	store := &mockRepository{}
	store.On("ListBlobs", mock.Anything, model.ListBlobsRequest{Type: model.TypeLocks}).Return(nil, nil).Once()
	h := New(serving(store))

	// Act
	resp, err := h.ListBlobs(context.Background(), api.ListBlobsRequestObject{ArcaveObjectID: testRepoName, Type: api.Locks})

	// Assert
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	require.NoError(t, resp.VisitListBlobsResponse(rec))
	assert.Equal(t, "[]", rec.Body.String())
	store.AssertExpectations(t)
}

// TestHandler_ListBlobs_UnknownType pins that a namespace this protocol does not have
// is a 404, not a 400, and never reaches the repository — the empty mock is the assertion.
func TestHandler_ListBlobs_UnknownType(t *testing.T) {
	// Arrange
	store := &mockRepository{}
	h := New(serving(store))

	// Act
	resp, err := h.ListBlobs(context.Background(), api.ListBlobsRequestObject{ArcaveObjectID: testRepoName, Type: "packs"})

	// Assert
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	require.NoError(t, resp.VisitListBlobsResponse(rec))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	store.AssertExpectations(t)
}

// TestHandler_HeadBlobs pins the one operation that answers without asking: both
// answers come from the address alone, so an empty mock covers each.
func TestHandler_HeadBlobs(t *testing.T) {
	tests := []struct {
		name       string
		objectType api.ObjectTypePath
		wantStatus int
	}{
		{name: "a namespace that exists", objectType: api.Data, wantStatus: http.StatusOK},
		{name: "a namespace that does not", objectType: "packs", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			h := New(serving(store))

			// Act
			resp, err := h.HeadBlobs(context.Background(), api.HeadBlobsRequestObject{ArcaveObjectID: testRepoName, Type: tt.objectType})

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitHeadBlobsResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Empty(t, rec.Body.String())
			store.AssertExpectations(t)
		})
	}
}

func TestHandler_StatBlob(t *testing.T) {
	tests := []struct {
		name       string
		objectType api.ObjectTypePath
		objectName string
		// stat, when set, is the repository's answer; a nil one is a case whose
		// address never resolves, so nothing is asked.
		stat       func(*mockRepository)
		wantStatus int
		wantLength string
	}{
		{
			name: "an object that is there", objectType: api.Data, objectName: string(testID),
			stat: func(m *mockRepository) {
				m.On("StatBlob", mock.Anything, model.StatBlobRequest{Type: model.TypeData, ID: testID}).
					Return(int64(16), nil).Once()
			},
			wantStatus: http.StatusOK, wantLength: "16",
		},
		{
			name: "an object that is not", objectType: api.Index, objectName: string(testID),
			stat: func(m *mockRepository) {
				m.On("StatBlob", mock.Anything, model.StatBlobRequest{Type: model.TypeIndex, ID: testID}).
					Return(int64(0), errNotFound).Once()
			},
			wantStatus: http.StatusNotFound,
		},
		{name: "a namespace that does not exist", objectType: "packs", objectName: string(testID), wantStatus: http.StatusNotFound},
		{name: "a name that is not an object name", objectType: api.Data, objectName: "nope", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			if tt.stat != nil {
				tt.stat(store)
			}
			h := New(serving(store))
			request := api.StatBlobRequestObject{ArcaveObjectID: testRepoName, Type: tt.objectType, ResticObjectID: tt.objectName}

			// Act
			resp, err := h.StatBlob(context.Background(), request)

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitStatBlobResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantLength, rec.Header().Get("Content-Length"))
			assert.Empty(t, rec.Body.String())
			store.AssertExpectations(t)
		})
	}
}

// TestHandler_GetBlob is the read path — whole, partial, unsatisfiable — and pins the
// interval reaching the repository with its status, and the order of the two calls.
func TestHandler_GetBlob(t *testing.T) {
	tests := []struct {
		name             string
		rangeHeader      *string
		wantStatus       int
		wantBody         string
		wantContentRange string
		wantLength       string
		// wantOpen is false where the size alone decides: a stream nobody reads
		// would cost a round trip per refused range.
		wantOpen    bool
		wantOpenAt  int64
		wantOpenLen int64
	}{
		{
			name:       "no Range is the whole object",
			wantStatus: http.StatusOK,
			wantBody:   string(blobBody),
			wantLength: "16",
			wantOpen:   true, wantOpenAt: 0, wantOpenLen: 16,
		},
		{
			name:             "a chunk out of the middle",
			rangeHeader:      utils.New("bytes=4-7"),
			wantStatus:       http.StatusPartialContent,
			wantBody:         "4567",
			wantContentRange: "bytes 4-7/16",
			wantLength:       "4",
			wantOpen:         true, wantOpenAt: 4, wantOpenLen: 4,
		},
		{
			name:             "an open ended range",
			rangeHeader:      utils.New("bytes=10-"),
			wantStatus:       http.StatusPartialContent,
			wantBody:         "abcdef",
			wantContentRange: "bytes 10-15/16",
			wantLength:       "6",
			wantOpen:         true, wantOpenAt: 10, wantOpenLen: 6,
		},
		{
			name:             "a range past the end is clamped",
			rangeHeader:      utils.New("bytes=12-99"),
			wantStatus:       http.StatusPartialContent,
			wantBody:         "cdef",
			wantContentRange: "bytes 12-15/16",
			wantLength:       "4",
			wantOpen:         true, wantOpenAt: 12, wantOpenLen: 4,
		},
		{
			name:             "a range that begins past the end",
			rangeHeader:      utils.New("bytes=16-20"),
			wantStatus:       http.StatusRequestedRangeNotSatisfiable,
			wantContentRange: "bytes */16",
		},
		{
			// ServeContent answers a malformed Range with a 416; RFC 9110 says
			// to ignore it, and this is where the two part company.
			name:        "a malformed Range is ignored",
			rangeHeader: utils.New("bytes=abc"),
			wantStatus:  http.StatusOK,
			wantBody:    string(blobBody),
			wantLength:  "16",
			wantOpen:    true, wantOpenAt: 0, wantOpenLen: 16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			body := newStream(tt.wantBody)
			store := &mockRepository{}
			stat := store.On("StatBlob", mock.Anything, model.StatBlobRequest{Type: model.TypeData, ID: testID}).
				Return(int64(len(blobBody)), nil).Once()
			if tt.wantOpen {
				read := model.OpenBlobRangeRequest{
					Type:   model.TypeData,
					ID:     testID,
					Offset: tt.wantOpenAt,
					Length: tt.wantOpenLen,
				}
				open := store.On("OpenBlobRange", mock.Anything, read).
					Return(body, nil).Once()
				mock.InOrder(stat, open)
			}
			h := New(serving(store))
			request := api.GetBlobRequestObject{
				ArcaveObjectID: testRepoName,
				Type:           api.Data,
				ResticObjectID: string(testID),
				Params:         api.GetBlobParams{Range: tt.rangeHeader},
			}

			// Act
			resp, err := h.GetBlob(context.Background(), request)

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitGetBlobResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantBody, rec.Body.String())
			assert.Equal(t, tt.wantContentRange, rec.Header().Get("Content-Range"))
			assert.Equal(t, tt.wantLength, rec.Header().Get("Content-Length"))
			// An unsatisfiable range declares no OpenBlobRange, so opening one fails here.
			store.AssertExpectations(t)

			if !tt.wantOpen {
				return
			}
			assert.True(t, body.closed, "the object stream was not given back")
			assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
			assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
		})
	}
}

func TestHandler_GetBlob_NotFound(t *testing.T) {
	tests := []struct {
		name       string
		objectType api.ObjectTypePath
		objectName string
		// stat is set only where the address resolves and the object itself is
		// absent; the rest never reach the repository.
		stat func(*mockRepository)
	}{
		{
			name: "an object that is not there", objectType: api.Index, objectName: string(testID),
			stat: func(m *mockRepository) {
				m.On("StatBlob", mock.Anything, model.StatBlobRequest{Type: model.TypeIndex, ID: testID}).
					Return(int64(0), errNotFound).Once()
			},
		},
		{name: "a namespace that does not exist", objectType: "packs", objectName: string(testID)},
		{name: "a name one character short", objectType: api.Data, objectName: string(testID)[:63]},
		{name: "a name in upper case", objectType: api.Data, objectName: strings.ToUpper(string(testID))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			if tt.stat != nil {
				tt.stat(store)
			}
			h := New(serving(store))
			request := api.GetBlobRequestObject{ArcaveObjectID: testRepoName, Type: tt.objectType, ResticObjectID: tt.objectName}

			// Act
			resp, err := h.GetBlob(context.Background(), request)

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitGetBlobResponse(rec))
			assert.Equal(t, http.StatusNotFound, rec.Code)
			// No OpenBlobRange is declared, so a 404 that opened a stream fails here.
			store.AssertExpectations(t)
		})
	}
}

// TestHandler_GetBlob_BodyClosedOnFailedTransfer pins what the generated Visit is relied
// on for: unwound, a client hanging up mid-pack leaks one open object per request.
func TestHandler_GetBlob_BodyClosedOnFailedTransfer(t *testing.T) {
	// Arrange
	body := &stream{Reader: failingReader{}}
	resp := api.GetBlob200ApplicationoctetStreamResponse{
		Body:          body,
		Headers:       api.GetBlob200ResponseHeaders{AcceptRanges: "bytes"},
		ContentLength: 16,
	}

	// Act
	err := resp.VisitGetBlobResponse(httptest.NewRecorder())

	// Assert
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.True(t, body.closed, "the stream was not given back after a failed transfer")
}

// TestHandler_SaveBlob pins that the upload is handed over as it arrived: a pack
// is some 16 MiB, and nothing on this path needs to see the bytes.
func TestHandler_SaveBlob(t *testing.T) {
	t.Run("an upload", func(t *testing.T) {
		// Arrange
		body := strings.NewReader("a pack")
		store := &mockRepository{}
		store.On("SaveBlob", mock.Anything, model.SaveBlobRequest{Type: model.TypeData, ID: testID, Body: body}).
			Return(nil).Once()
		h := New(serving(store))
		request := api.SaveBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: string(testID), Body: body}

		// Act
		resp, err := h.SaveBlob(context.Background(), request)

		// Assert
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		require.NoError(t, resp.VisitSaveBlobResponse(rec))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, len("a pack"), body.Len(), "the body was read before the repository saw it")
		store.AssertExpectations(t)
	})

	t.Run("an upload under a name that is not one", func(t *testing.T) {
		// Arrange
		store := &mockRepository{}
		h := New(serving(store))
		request := api.SaveBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: "nope", Body: strings.NewReader("a pack")}

		// Act
		resp, err := h.SaveBlob(context.Background(), request)

		// Assert
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		require.NoError(t, resp.VisitSaveBlobResponse(rec))
		assert.Equal(t, http.StatusNotFound, rec.Code)
		store.AssertExpectations(t)
	})
}

func TestHandler_DeleteBlob(t *testing.T) {
	tests := []struct {
		name       string
		objectName string
		// remove is unset for the address that never resolves — the one case that
		// must not reach the repository at all.
		remove     func(*mockRepository)
		wantStatus int
	}{
		{
			name: "an object that is there", objectName: string(testID),
			remove: func(m *mockRepository) {
				m.On("DeleteBlob", mock.Anything, model.DeleteBlobRequest{Type: model.TypeData, ID: testID}).
					Return(nil).Once()
			},
			wantStatus: http.StatusOK,
		},
		{
			// Idempotent: a retry landing after the first attempt must not fail a prune.
			name: "an object that is already gone", objectName: string(testID),
			remove: func(m *mockRepository) {
				m.On("DeleteBlob", mock.Anything, model.DeleteBlobRequest{Type: model.TypeData, ID: testID}).
					Return(errNotFound).Once()
			},
			wantStatus: http.StatusOK,
		},
		{
			// An address that could never name an object is not one already gone.
			name: "a name that is not an object name", objectName: "nope",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			if tt.remove != nil {
				tt.remove(store)
			}
			h := New(serving(store))
			request := api.DeleteBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: tt.objectName}

			// Act
			resp, err := h.DeleteBlob(context.Background(), request)

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, resp.VisitDeleteBlobResponse(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			store.AssertExpectations(t)
		})
	}
}

// TestHandler_SentinelsNeverEscape is the guard the type system cannot be: a sentinel
// returned as an error compiles into a silent 500 where a 404 or 403 was declared.
func TestHandler_SentinelsNeverEscape(t *testing.T) {
	operations := []struct {
		name string
		run  func(context.Context, api.StrictServerInterface) error
	}{
		{"CreateRepository", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.CreateRepository(ctx, api.CreateRepositoryRequestObject{ArcaveObjectID: testRepoName, Params: api.CreateRepositoryParams{Create: "true"}})
			return err
		}},
		{"DeleteRepository", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.DeleteRepository(ctx, api.DeleteRepositoryRequestObject{ArcaveObjectID: testRepoName})
			return err
		}},
		{"StatConfig", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.StatConfig(ctx, api.StatConfigRequestObject{ArcaveObjectID: testRepoName})
			return err
		}},
		{"GetConfig", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.GetConfig(ctx, api.GetConfigRequestObject{ArcaveObjectID: testRepoName})
			return err
		}},
		{"SaveConfig", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.SaveConfig(ctx, api.SaveConfigRequestObject{ArcaveObjectID: testRepoName, Body: strings.NewReader("written")})
			return err
		}},
		{"DeleteConfig", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.DeleteConfig(ctx, api.DeleteConfigRequestObject{ArcaveObjectID: testRepoName})
			return err
		}},
		{"ListBlobs", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.ListBlobs(ctx, api.ListBlobsRequestObject{ArcaveObjectID: testRepoName, Type: api.Data})
			return err
		}},
		{"HeadBlobs", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.HeadBlobs(ctx, api.HeadBlobsRequestObject{ArcaveObjectID: testRepoName, Type: api.Data})
			return err
		}},
		{"StatBlob", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.StatBlob(ctx, api.StatBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: string(testID)})
			return err
		}},
		{"GetBlob", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.GetBlob(ctx, api.GetBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: string(testID)})
			return err
		}},
		{"SaveBlob", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.SaveBlob(ctx, api.SaveBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: string(testID), Body: strings.NewReader("a pack")})
			return err
		}},
		{"DeleteBlob", func(ctx context.Context, h api.StrictServerInterface) error {
			_, err := h.DeleteBlob(ctx, api.DeleteBlobRequestObject{ArcaveObjectID: testRepoName, Type: api.Data, ResticObjectID: string(testID)})
			return err
		}},
	}

	repositories := []struct {
		name    string
		initErr error
	}{
		// Everything a read, a stat or a delete can ask for is absent.
		{name: "a repository that holds nothing"},
		// Create and SaveConfig raise theirs; the rest still answer.
		{name: "a repository that already holds a config", initErr: model.ErrConfigExists},
	}

	for _, r := range repositories {
		t.Run(r.name, func(t *testing.T) {
			for _, op := range operations {
				t.Run(op.name, func(t *testing.T) {
					// Arrange
					store := repositoryRaising(r.initErr)
					h := New(serving(store))

					// Act
					err := op.run(context.Background(), h)

					// Assert
					require.NoError(t, err, "a condition this operation knows was returned as an error, and is a 500 on the wire")
					store.AssertExpectations(t)
				})
			}
		})
	}
}

// visitor is an operation's own renderer, the method the generated wrapper would
// call.
type visitor func(http.ResponseWriter) error

// TestHandler_UnknownRepository pins the second door of every operation: an
// unserved name is that operation's own 404, and the repository is never asked —
// the empty mock is that half.
//
// All twelve rather than a representative few, for the two rows that are not
// ordinary 404s: HeadBlobs answers from its path alone, so a missing resolution
// there is a silent 200, and DeleteRepository must stay 501.
func TestHandler_UnknownRepository(t *testing.T) {
	tests := []struct {
		name       string
		run        func(context.Context, api.StrictServerInterface) (visitor, error)
		wantStatus int
	}{
		{name: "CreateRepository", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.CreateRepository(ctx, api.CreateRepositoryRequestObject{
				ArcaveObjectID: unknownRepoName,
				Params:         api.CreateRepositoryParams{Create: "true"},
			})
			if err != nil {
				return nil, err
			}
			return resp.VisitCreateRepositoryResponse, nil
		}},
		{name: "DeleteRepository", wantStatus: http.StatusNotImplemented, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.DeleteRepository(ctx, api.DeleteRepositoryRequestObject{ArcaveObjectID: unknownRepoName})
			if err != nil {
				return nil, err
			}
			return resp.VisitDeleteRepositoryResponse, nil
		}},
		{name: "StatConfig", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.StatConfig(ctx, api.StatConfigRequestObject{ArcaveObjectID: unknownRepoName})
			if err != nil {
				return nil, err
			}
			return resp.VisitStatConfigResponse, nil
		}},
		{name: "GetConfig", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.GetConfig(ctx, api.GetConfigRequestObject{ArcaveObjectID: unknownRepoName})
			if err != nil {
				return nil, err
			}
			return resp.VisitGetConfigResponse, nil
		}},
		{name: "SaveConfig", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.SaveConfig(ctx, api.SaveConfigRequestObject{ArcaveObjectID: unknownRepoName, Body: strings.NewReader("written")})
			if err != nil {
				return nil, err
			}
			return resp.VisitSaveConfigResponse, nil
		}},
		{name: "DeleteConfig", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.DeleteConfig(ctx, api.DeleteConfigRequestObject{ArcaveObjectID: unknownRepoName})
			if err != nil {
				return nil, err
			}
			return resp.VisitDeleteConfigResponse, nil
		}},
		{name: "ListBlobs", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.ListBlobs(ctx, api.ListBlobsRequestObject{ArcaveObjectID: unknownRepoName, Type: api.Data})
			if err != nil {
				return nil, err
			}
			return resp.VisitListBlobsResponse, nil
		}},
		{name: "HeadBlobs", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.HeadBlobs(ctx, api.HeadBlobsRequestObject{ArcaveObjectID: unknownRepoName, Type: api.Data})
			if err != nil {
				return nil, err
			}
			return resp.VisitHeadBlobsResponse, nil
		}},
		{name: "StatBlob", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.StatBlob(ctx, api.StatBlobRequestObject{ArcaveObjectID: unknownRepoName, Type: api.Data, ResticObjectID: string(testID)})
			if err != nil {
				return nil, err
			}
			return resp.VisitStatBlobResponse, nil
		}},
		{name: "GetBlob", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.GetBlob(ctx, api.GetBlobRequestObject{ArcaveObjectID: unknownRepoName, Type: api.Data, ResticObjectID: string(testID)})
			if err != nil {
				return nil, err
			}
			return resp.VisitGetBlobResponse, nil
		}},
		{name: "SaveBlob", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.SaveBlob(ctx, api.SaveBlobRequestObject{
				ArcaveObjectID: unknownRepoName,
				Type:           api.Data,
				ResticObjectID: string(testID),
				Body:           strings.NewReader("a pack"),
			})
			if err != nil {
				return nil, err
			}
			return resp.VisitSaveBlobResponse, nil
		}},
		{name: "DeleteBlob", wantStatus: http.StatusNotFound, run: func(ctx context.Context, h api.StrictServerInterface) (visitor, error) {
			resp, err := h.DeleteBlob(ctx, api.DeleteBlobRequestObject{ArcaveObjectID: unknownRepoName, Type: api.Data, ResticObjectID: string(testID)})
			if err != nil {
				return nil, err
			}
			return resp.VisitDeleteBlobResponse, nil
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &mockRepository{}
			h := New(serving(store))

			// Act
			visit, err := tt.run(context.Background(), h)

			// Assert
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			require.NoError(t, visit(rec))
			assert.Equal(t, tt.wantStatus, rec.Code)
			store.AssertExpectations(t)
		})
	}
}

// TestHandler_UnknownRepository_IsJournalled: a name arcave never registered is a
// bug on arcave's own side, so it is an ERROR and not only a status.
func TestHandler_UnknownRepository_IsJournalled(t *testing.T) {
	// Arrange
	var journal bytes.Buffer
	ctx := zerolog.New(&journal).WithContext(context.Background())
	h := New(serving(&mockRepository{}))

	// Act
	_, err := h.StatConfig(ctx, api.StatConfigRequestObject{ArcaveObjectID: unknownRepoName})

	// Assert
	require.NoError(t, err)
	assert.Contains(t, journal.String(), `"level":"error"`)
	assert.Contains(t, journal.String(), "repository is not found")
	assert.Contains(t, journal.String(), unknownRepoName)
}

// TestHandler_ResolverFailure: a lookup failing for a reason that is not the
// sentinel is no declared answer of this protocol, so it is returned as the 500.
func TestHandler_ResolverFailure(t *testing.T) {
	// Arrange
	h := New(failingResolver{})

	// Act
	resp, err := h.StatBlob(context.Background(), api.StatBlobRequestObject{
		ArcaveObjectID: testRepoName,
		Type:           api.Data,
		ResticObjectID: string(testID),
	})

	// Assert
	require.ErrorIs(t, err, errStore)
	assert.Nil(t, resp)
}

// repositoryRaising answers every operation with the condition it declares: initErr from
// an init's two calls, the absent-object sentinel from everything addressed by a name.
func repositoryRaising(initErr error) *mockRepository {
	store := &mockRepository{}
	store.On("Create", mock.Anything).Return(initErr).Maybe()
	store.On("StatConfig", mock.Anything).Return(int64(0), errNotFound).Maybe()
	store.On("OpenConfig", mock.Anything).Return(nil, errNotFound).Maybe()
	store.On("SaveConfig", mock.Anything, anyBody).Return(initErr).Maybe()
	store.On("DeleteConfig", mock.Anything).Return(errNotFound).Maybe()
	store.On("ListBlobs", mock.Anything, model.ListBlobsRequest{Type: model.TypeData}).Return(nil, nil).Maybe()
	store.On("StatBlob", mock.Anything, model.StatBlobRequest{Type: model.TypeData, ID: testID}).
		Return(int64(0), errNotFound).Maybe()
	store.On("SaveBlob", mock.Anything, anySaveOf(model.TypeData, testID)).Return(nil).Maybe()
	store.On("DeleteBlob", mock.Anything, model.DeleteBlobRequest{Type: model.TypeData, ID: testID}).
		Return(errNotFound).Maybe()
	return store
}

// testID is a name of the shape restic writes: 64 lowercase hex characters.
var testID = model.ObjectID(strings.Repeat("0a1b2c3d", 8))

// anyBody matches an upload whose identity is not stable, built inside the case
// that passes it; that there is a body at all is what it asserts.
var anyBody = mock.MatchedBy(func(r io.Reader) bool { return r != nil })

// anySaveOf is anyBody for the save request that now carries it: the address is
// matched exactly, and only the stream is the part no case can name.
func anySaveOf(t model.ObjectType, id model.ObjectID) any {
	return mock.MatchedBy(func(req model.SaveBlobRequest) bool {
		return req.Type == t && req.ID == id && req.Body != nil
	})
}

// stream is what a repository answers a read with; it records whether it was
// closed, the half of a read no status code shows.
type stream struct {
	io.Reader
	closed bool
}

func newStream(body string) *stream { return &stream{Reader: strings.NewReader(body)} }

func (s *stream) Close() error {
	s.closed = true
	return nil
}

// failingReader is a transfer that dies partway through: the case where a status
// is already on the wire and the only thing still owed is the stream.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
