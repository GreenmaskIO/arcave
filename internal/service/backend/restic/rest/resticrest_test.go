package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/restic/rest-server/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The path gate is the first thing every request meets: a name that is not
// exactly 64 hex characters, a type outside the white list, or anything with a
// separator in it must be refused before the store is touched at all.
//
// Half of these cases are also what pins the gate in front of the mux rather
// than beside it — a bare mux answers a 307 to "/nope", to "/data" and to
// "/data/../config", and its own "404 page not found" body to "/data/aa/<id>".
// Every one of them is a flat 404 in rest-server, and every one of them reaches
// the mux only if the gate is removed.
func TestHandler_ServeHTTP_PathValidation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		want   int
	}{
		{name: "valid blob path", method: http.MethodHead, target: "/data/" + blobID, want: http.StatusNotFound},
		{name: "unknown type", method: http.MethodGet, target: "/packs/" + blobID, want: http.StatusNotFound},
		{name: "type is not a blob type", method: http.MethodGet, target: "/config/" + blobID, want: http.StatusNotFound},
		{name: "id too short", method: http.MethodGet, target: "/data/" + blobID[:63], want: http.StatusNotFound},
		{name: "id too long", method: http.MethodGet, target: "/data/" + blobID + "a", want: http.StatusNotFound},
		{name: "id is not hex", method: http.MethodGet, target: "/data/" + strings.Repeat("g", 64), want: http.StatusNotFound},
		{name: "id in upper case", method: http.MethodGet, target: "/data/" + strings.ToUpper(letteredBlobID), want: http.StatusNotFound},
		{name: "nested id", method: http.MethodGet, target: "/data/aa/" + blobID, want: http.StatusNotFound},
		{name: "traversal in the id", method: http.MethodGet, target: "/data/../config", want: http.StatusNotFound},
		{name: "traversal above the store", method: http.MethodGet, target: "/data/..%2f..%2fetc%2fpasswd", want: http.StatusNotFound},
		{name: "unknown root path", method: http.MethodGet, target: "/nope", want: http.StatusNotFound},
		{name: "config with a trailing slash", method: http.MethodGet, target: "/config/", want: http.StatusNotFound},
		{name: "listing of an unknown type", method: http.MethodGet, target: "/packs/", want: http.StatusNotFound},
		{name: "a type without its trailing slash is not a listing", method: http.MethodGet, target: "/data", want: http.StatusNotFound},
		{name: "an empty segment", method: http.MethodGet, target: "//config", want: http.StatusNotFound},
		{name: "a dot segment", method: http.MethodGet, target: "/./config", want: http.StatusNotFound},
		{name: "an encoded separator inside the id", method: http.MethodGet, target: "/data/" + blobID[:2] + "%2f" + blobID[2:], want: http.StatusNotFound},
		// The path is judged before the method is: an id that is not one gets
		// the path's 404, never the 405 a well-formed path would have earned.
		{name: "wrong method on an invalid id", method: http.MethodPut, target: "/data/xyz", want: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, _ := newStoreHandler(t)

			// Act
			rec := do(t, h, tt.method, tt.target, "")

			// Assert
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// The grammar itself, exercised without the HTTP plumbing: the cases above prove
// the gate is wired in front of the mux, these prove it draws the line in the
// right place. Both are worth having — a gate that accepts everything passes the
// first set as long as the mux still 404s, and one that accepts nothing passes it
// too.
func TestValidPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "repository root", path: "/", want: true},
		{name: "config", path: "/config", want: true},
		{name: "listing", path: "/data/", want: true},
		{name: "object", path: "/data/" + blobID, want: true},

		{name: "empty", path: "", want: false},
		{name: "no leading slash", path: "data/" + blobID, want: false},
		{name: "unknown root path", path: "/nope", want: false},
		{name: "unknown type", path: "/packs/" + blobID, want: false},
		{name: "type in upper case", path: "/DATA/", want: false},
		{name: "config is not a type", path: "/config/" + blobID, want: false},
		{name: "config with a trailing slash", path: "/config/", want: false},
		{name: "type without its trailing slash", path: "/data", want: false},
		{name: "id one character short", path: "/data/" + blobID[:objectIDLen-1], want: false},
		{name: "id one character long", path: "/data/" + blobID + "a", want: false},
		{name: "id in upper case", path: "/data/" + strings.ToUpper(letteredBlobID), want: false},
		{name: "id in hex letters", path: "/data/" + letteredBlobID, want: true},
		{name: "id outside hex", path: "/data/" + strings.Repeat("g", objectIDLen), want: false},
		{name: "id sharded as rest-server would", path: "/data/aa/" + blobID, want: false},
		{name: "empty segment under a type", path: "/data//", want: false},
		{name: "empty leading segment", path: "//config", want: false},
		{name: "dot segment", path: "/./config", want: false},
		{name: "traversal", path: "/data/../config", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := validPath(tt.path)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// Every type on the white list is a path in both of its shapes, and the list is
// rest-server's. Pinning it against repo.ObjectTypes is what would fail if a type
// were dropped from ours — a listing restic needs answering 404 for the life of
// the repository.
func TestValidPath_ObjectTypes(t *testing.T) {
	// Assert
	assert.ElementsMatch(t, repo.ObjectTypes, objectTypes)

	for _, objectType := range objectTypes {
		t.Run(objectType, func(t *testing.T) {
			assert.True(t, validPath("/"+objectType+"/"), "listing")
			assert.True(t, validPath("/"+objectType+"/"+blobID), "object")
		})
	}
}

// A path is judged, and then routed, by its unescaped form — the form
// rest-server matches its own regexp against — so an encoding that spells a
// valid path is that path.
//
// The gate and the mux would otherwise judge two different strings: the mux
// routes on the escaped path, where "/data%2f" is one segment and no listing at
// all. That divergence is what withPathGate closes by handing the mux the path
// it validated, and the last case is what pins it.
func TestHandler_ServeHTTP_PercentEncodedPath(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		body     string
		target   string
		wantBody string
	}{
		{name: "encoded config", key: "config", body: "[repository]", target: "/%63onfig", wantBody: "[repository]"},
		{name: "encoded blob name", key: "data/" + blobID, body: "pack-bytes", target: "/data/%31" + blobID[1:], wantBody: "pack-bytes"},
		{name: "encoded trailing slash is still a listing", key: "data/" + blobID, body: "pack-bytes", target: "/data%2f", wantBody: `["` + blobID + `"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, store := newStoreHandler(t)
			put(t, store, tt.key, tt.body)

			// Act
			rec := do(t, h, http.MethodGet, tt.target, "")

			// Assert
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantBody, rec.Body.String())
		})
	}
}

// A known path reached with the wrong method is a 405 that names the methods it
// does accept — restic's HTTP client is a general one, and an Allow-less 405 is
// a protocol violation.
//
// The Allow the mux writes is sorted, where rest-server writes the literal
// "HEAD, GET, POST, DELETE"; RFC 9110 gives the order no meaning, so the oracle
// compares the header as a set and these cases pin the order this server
// actually emits.
func TestHandler_ServeHTTP_MethodNotAllowed(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		wantAllow string
	}{
		{name: "root takes only POST", method: http.MethodGet, target: "/", wantAllow: "POST"},
		{name: "root does not take DELETE", method: http.MethodDelete, target: "/", wantAllow: "POST"},
		{name: "listing takes only GET", method: http.MethodPost, target: "/data/", wantAllow: "GET, HEAD"},
		{name: "config takes the four", method: http.MethodPut, target: "/config", wantAllow: "DELETE, GET, HEAD, POST"},
		{name: "blob takes the four", method: http.MethodPut, target: "/data/" + blobID, wantAllow: "DELETE, GET, HEAD, POST"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, _ := newStoreHandler(t)

			// Act
			rec := do(t, h, tt.method, tt.target, "")

			// Assert
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			assert.Equal(t, tt.wantAllow, rec.Header().Get("Allow"))
		})
	}
}

// A listing answers HEAD, where rest-server refuses it with a 405: GET-implies-
// HEAD is http.ServeMux semantics, and serving the headers of a listing is the
// more correct HTTP of the two. The recorder shows the body a real server would
// strip, so only the status and the type are asserted.
func TestHandler_ServeHTTP_HeadOnAListing(t *testing.T) {
	// Arrange
	h, store := newStoreHandler(t)
	put(t, store, "data/"+blobID, "abc")

	// Act
	rec := do(t, h, http.MethodHead, "/data/", "")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, mimeTypeAPIV1, rec.Header().Get("Content-Type"))
}

// An init is POST / with create=true; anything else on / is a 400.
func TestHandler_createRepo(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{name: "init", target: "/?create=true", want: http.StatusOK},
		{name: "without the create flag", target: "/", want: http.StatusBadRequest},
		{name: "with a false create flag", target: "/?create=false", want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, _ := newStoreHandler(t)

			// Act
			rec := do(t, h, http.MethodPost, tt.target, "")

			// Assert
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// A fresh repository must be listable and statable straight after init, even
// though init created nothing: "absent" and "empty" are the same state here.
func TestHandler_createRepo_LeavesAnEmptyRepository(t *testing.T) {
	// Arrange
	h, _ := newStoreHandler(t)

	// Act
	created := do(t, h, http.MethodPost, "/?create=true", "")
	listed := do(t, h, http.MethodGet, "/locks/", "")
	config := do(t, h, http.MethodHead, "/config", "")

	// Assert
	require.Equal(t, http.StatusOK, created.Code)
	assert.Equal(t, http.StatusOK, listed.Code)
	assert.JSONEq(t, `[]`, listed.Body.String())
	assert.Equal(t, http.StatusNotFound, config.Code, "an initialised repository still has no config until restic writes one")
}

// HEAD answers the object's exact size in Content-Length; absent is a 404.
func TestHandler_ServeHTTP_Stat(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		body       string
		target     string
		wantStatus int
		wantLength string
	}{
		{name: "config", key: "config", body: "[config]", target: "/config", wantStatus: http.StatusOK, wantLength: "8"},
		{name: "blob", key: "data/" + blobID, body: "0123456789", target: "/data/" + blobID, wantStatus: http.StatusOK, wantLength: "10"},
		{name: "empty blob", key: "locks/" + blobID, body: "", target: "/locks/" + blobID, wantStatus: http.StatusOK, wantLength: "0"},
		{name: "missing config", target: "/config", wantStatus: http.StatusNotFound},
		{name: "missing blob", target: "/data/" + blobID, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, store := newStoreHandler(t)
			if tt.key != "" {
				put(t, store, tt.key, tt.body)
			}

			// Act
			rec := do(t, h, http.MethodHead, tt.target, "")

			// Assert
			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusOK {
				assert.Equal(t, tt.wantLength, rec.Header().Get("Content-Length"))
			}
		})
	}
}

// GET /config serves the config verbatim; absent is a 404.
func TestHandler_getConfig(t *testing.T) {
	// Arrange
	h, store := newStoreHandler(t)
	put(t, store, "config", "[repository]")

	// Act
	present := do(t, h, http.MethodGet, "/config", "")
	h2, _ := newStoreHandler(t)
	absent := do(t, h2, http.MethodGet, "/config", "")

	// Assert
	require.Equal(t, http.StatusOK, present.Code)
	assert.Equal(t, "[repository]", present.Body.String())
	assert.Equal(t, http.StatusNotFound, absent.Code)
}

// POST /config is init's write, and it must never overwrite: a second init
// against a repository that already holds data is the mistake the 403 exists to
// stop.
func TestHandler_saveConfig(t *testing.T) {
	// Arrange
	h, store := newStoreHandler(t)

	// Act
	first := do(t, h, http.MethodPost, "/config", "[repository]")
	second := do(t, h, http.MethodPost, "/config", "[other]")

	// Assert
	require.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusForbidden, second.Code)

	stored, err := store.GetObject(context.Background(), "config")
	require.NoError(t, err)
	defer func() { require.NoError(t, stored.Close()) }()
	got, err := io.ReadAll(stored)
	require.NoError(t, err)
	assert.Equal(t, "[repository]", string(got), "the refused POST must not have touched the config")
}

// The same name twice is an overwrite, never a refusal — restic retries
// uploads, and the name is content-addressed.
func TestHandler_saveBlob(t *testing.T) {
	// Arrange
	h, store := newStoreHandler(t)

	// Act: a blob, then the same name again — restic retries uploads, and the
	// name is content-addressed, so the second one is not refused.
	first := do(t, h, http.MethodPost, "/data/"+blobID, "pack-bytes")
	second := do(t, h, http.MethodPost, "/data/"+blobID, "pack-bytes-again")

	// Assert
	require.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusOK, second.Code)

	st, err := store.Stat("data/" + blobID)
	require.NoError(t, err)
	assert.True(t, st.Exist)
	assert.EqualValues(t, len("pack-bytes-again"), st.Size)
}

// DELETE is idempotent on purpose: restic retries deletes during prune and
// unlock, and a retry that lands after the first attempt succeeded must not fail
// the operation.
func TestHandler_ServeHTTP_Delete(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		target string
	}{
		{name: "blob", key: "data/" + blobID, target: "/data/" + blobID},
		{name: "lock", key: "locks/" + blobID, target: "/locks/" + blobID},
		{name: "config", key: "config", target: "/config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, store := newStoreHandler(t)
			put(t, store, tt.key, "payload")

			// Act
			first := do(t, h, http.MethodDelete, tt.target, "")
			second := do(t, h, http.MethodDelete, tt.target, "")

			// Assert
			assert.Equal(t, http.StatusOK, first.Code)
			assert.Equal(t, http.StatusOK, second.Code, "deleting what is already gone is a success")

			exists, err := store.Exists(context.Background(), tt.key)
			require.NoError(t, err)
			assert.False(t, exists)
		})
	}
}

// A listing speaks both API versions — v1 is names, v2 carries sizes — and an
// empty one is [] in either.
func TestHandler_listBlobs(t *testing.T) {
	tests := []struct {
		name            string
		accept          string
		target          string
		wantContentType string
		wantBody        string
	}{
		{
			name:            "v1 by default",
			target:          "/data/",
			wantContentType: mimeTypeAPIV1,
			wantBody:        `["` + blobID + `","` + otherBlobID + `"]`,
		},
		{
			name:            "v1 when asked for",
			accept:          mimeTypeAPIV1,
			target:          "/data/",
			wantContentType: mimeTypeAPIV1,
			wantBody:        `["` + blobID + `","` + otherBlobID + `"]`,
		},
		{
			name:            "v2 carries sizes",
			accept:          mimeTypeAPIV2,
			target:          "/data/",
			wantContentType: mimeTypeAPIV2,
			wantBody:        `[{"name":"` + blobID + `","size":3},{"name":"` + otherBlobID + `","size":5}]`,
		},
		{
			name:            "a type never written lists empty",
			accept:          mimeTypeAPIV2,
			target:          "/locks/",
			wantContentType: mimeTypeAPIV2,
			wantBody:        `[]`,
		},
		{
			name:            "an empty v1 listing is [] and not null",
			target:          "/snapshots/",
			wantContentType: mimeTypeAPIV1,
			wantBody:        `[]`,
		},
		{
			name:            "a type never matches its prefix-sharing sibling",
			accept:          mimeTypeAPIV1,
			target:          "/index/",
			wantContentType: mimeTypeAPIV1,
			wantBody:        `["` + blobID + `"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, store := newStoreHandler(t)
			put(t, store, "data/"+blobID, "abc")
			put(t, store, "data/"+otherBlobID, "abcde")
			put(t, store, "index/"+blobID, "xy")

			// Act
			var headers [][2]string
			if tt.accept != "" {
				headers = append(headers, [2]string{"Accept", tt.accept})
			}
			rec := do(t, h, http.MethodGet, tt.target, "", headers...)

			// Assert
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantContentType, rec.Header().Get("Content-Type"))
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
		})
	}
}

// A listing must hold only this repository's own flat keys: anything nested
// under the type prefix was not written here and is not a blob.
func TestHandler_listBlobs_SkipsNestedKeys(t *testing.T) {
	// Arrange
	h, store := newStoreHandler(t)
	put(t, store, "data/"+blobID, "abc")
	put(t, store, "data/aa/"+otherBlobID, "nested")

	// Act
	rec := do(t, h, http.MethodGet, "/data/", "", [2]string{"Accept", mimeTypeAPIV2})

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	var blobs []blob
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &blobs))
	assert.Equal(t, []blob{{Name: blobID, Size: 3}}, blobs)
}

// GET serves the blob whole or by Range, with http.ServeContent's bounds.
func TestHandler_getBlob(t *testing.T) {
	const payload = "0123456789abcdef"

	tests := []struct {
		name       string
		rangeValue string
		wantStatus int
		wantBody   string
		wantRange  string
	}{
		{name: "whole object", wantStatus: http.StatusOK, wantBody: payload},
		{name: "prefix", rangeValue: "bytes=0-3", wantStatus: http.StatusPartialContent, wantBody: "0123", wantRange: "bytes 0-3/16"},
		{name: "middle", rangeValue: "bytes=4-7", wantStatus: http.StatusPartialContent, wantBody: "4567", wantRange: "bytes 4-7/16"},
		{name: "open-ended", rangeValue: "bytes=10-", wantStatus: http.StatusPartialContent, wantBody: "abcdef", wantRange: "bytes 10-15/16"},
		{name: "suffix", rangeValue: "bytes=-4", wantStatus: http.StatusPartialContent, wantBody: "cdef", wantRange: "bytes 12-15/16"},
		{name: "clamped past the end", rangeValue: "bytes=12-99", wantStatus: http.StatusPartialContent, wantBody: "cdef", wantRange: "bytes 12-15/16"},
		{name: "wholly past the end", rangeValue: "bytes=16-20", wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */16"},
		{name: "unsatisfiable start", rangeValue: "bytes=99-", wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */16"},
		{name: "malformed range", rangeValue: "bytes=abc", wantStatus: http.StatusRequestedRangeNotSatisfiable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, store := newStoreHandler(t)
			put(t, store, "data/"+blobID, payload)

			// Act
			var headers [][2]string
			if tt.rangeValue != "" {
				headers = append(headers, [2]string{"Range", tt.rangeValue})
			}
			rec := do(t, h, http.MethodGet, "/data/"+blobID, "", headers...)

			// Assert
			require.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, rec.Body.String())
			}
			if tt.wantRange != "" {
				assert.Equal(t, tt.wantRange, rec.Header().Get("Content-Range"))
			}
			if tt.wantStatus != http.StatusRequestedRangeNotSatisfiable {
				assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
				assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
			}
			assert.Empty(t, rec.Header().Get("Last-Modified"), "the store has no meaningful modtime to report")
		})
	}
}

// Reading a blob that is not there is a 404.
func TestHandler_getBlob_Missing(t *testing.T) {
	// Arrange
	h, _ := newStoreHandler(t)

	// Act
	rec := do(t, h, http.MethodGet, "/data/"+blobID, "")

	// Assert
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// An empty object is served as an empty 200, not as a range error: the
// zero-length read never reaches the store, which would reject every range on it.
func TestHandler_getBlob_Empty(t *testing.T) {
	// Arrange
	h, store := newStoreHandler(t)
	put(t, store, "locks/"+blobID, "")

	// Act
	rec := do(t, h, http.MethodGet, "/locks/"+blobID, "")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String())
}

// The whole point of the seeker adapter: a ranged GET must transfer only the
// bytes asked for, or restic reading one blob out of a 50 MB pack would drag the
// whole pack across the tunnel.
func TestStoragerFile_Read_RangedGetFetchesOnlyTheRange(t *testing.T) {
	// Arrange
	st, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	counting := &countingStore{Storager: st}
	put(t, counting.Storager, "data/"+blobID, strings.Repeat("x", 1024)+"needle"+strings.Repeat("y", 1024))
	h := New(counting)

	// Act
	rec := do(t, h, http.MethodGet, "/data/"+blobID, "", [2]string{"Range", "bytes=1024-1029"})

	// Assert
	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "needle", rec.Body.String())
	assert.Equal(t, []rangeCall{{offset: 1024, length: -1}}, counting.calls,
		"exactly one ranged open, starting at the offset")
}

// rangeCall is one GetObjectRange the handler made.
type rangeCall struct {
	offset int64
	length int64
}

// countingStore records the ranged reads its handler performs. It embeds the
// Storager it decorates, so every other method is the real one.
type countingStore struct {
	storages.Storager
	calls []rangeCall
}

func (c *countingStore) GetObjectRange(ctx context.Context, filePath string, offset, length int64) (io.ReadCloser, error) {
	c.calls = append(c.calls, rangeCall{offset: offset, length: length})
	return c.Storager.GetObjectRange(ctx, filePath, offset, length)
}
