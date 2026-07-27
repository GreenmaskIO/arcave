package rest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/greenmaskio/storages/directory"
	"github.com/restic/rest-server/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is the differential oracle: the same request stream is replayed against
// this handler and against restic/rest-server's own repo.Handler — the server
// real restic clients are tested against — and the two answers are compared.
//
// It catches semantic drift without a restic binary, which makes it far cheaper
// than the conformance test next door and the reason that one is not the only
// guard. rest-server is a test-only dependency: it is pinned to the filesystem
// (os.Open, rename, fsync) and would defeat the whole point of serving a
// Storager, so it is never imported by production code.
//
// Where the two deliberately differ, the case says so and pins both answers, so
// a deviation that quietly disappears — or one that quietly appears — fails here.

// oracleStep is one request in the replayed stream.
type oracleStep struct {
	name    string
	method  string
	target  string
	body    []byte
	headers [][2]string
	// unordered compares the JSON body as a set: a listing's order is each
	// server's own business (rest-server answers in readdir order, this handler
	// sorted), and restic does not depend on it.
	unordered bool
	// deviation, when set, records a pinned difference instead of parity.
	deviation *oracleDeviation
}

// oracleDeviation pins both sides of a deliberate difference (§5 of the plan):
// the reason it exists, and the answer each server gives.
type oracleDeviation struct {
	reason        string
	wantStatus    int
	wantReference int
	// wantAllow and wantReferenceAllow pin a deviation the status does not show,
	// as sets. Both are left nil when Allow is not part of the difference.
	wantAllow          []string
	wantReferenceAllow []string
}

// headersCompared are the response headers the protocol carries meaning in.
// Everything else (Date, Content-Length synthesised by the recorder, …) is
// transport noise.
var headersCompared = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Allow"}

func TestHandler_ServeHTTP_MatchesRestServer(t *testing.T) {
	// Arrange
	ours, reference := newOraclePair(t)

	payload := binaryPayload(4096)
	blobKey := "/data/" + sha256Hex(payload)
	other := binaryPayload(64)
	otherKey := "/index/" + sha256Hex(other)
	config := []byte("[repository]\nversion = 2\n")

	steps := []oracleStep{
		{name: "init", method: http.MethodPost, target: "/?create=true"},
		{name: "init without the create flag", method: http.MethodPost, target: "/"},
		{name: "config before it is written", method: http.MethodHead, target: "/config"},
		{name: "get config before it is written", method: http.MethodGet, target: "/config"},
		{name: "delete config before it is written", method: http.MethodDelete, target: "/config"},
		{name: "write config", method: http.MethodPost, target: "/config", body: config},
		{name: "re-init over an existing config", method: http.MethodPost, target: "/config", body: config},
		{name: "config exists", method: http.MethodHead, target: "/config"},
		{name: "read config", method: http.MethodGet, target: "/config"},

		{name: "empty data listing v1", method: http.MethodGet, target: "/data/"},
		{name: "empty data listing v2", method: http.MethodGet, target: "/data/", headers: acceptV2()},

		{name: "upload a pack", method: http.MethodPost, target: blobKey, body: payload},
		{name: "pack exists", method: http.MethodHead, target: blobKey},
		{name: "read the pack", method: http.MethodGet, target: blobKey},
		{name: "read a blob out of the pack", method: http.MethodGet, target: blobKey, headers: rangeHeader("bytes=1024-1029")},
		{name: "read the pack's tail", method: http.MethodGet, target: blobKey, headers: rangeHeader("bytes=4090-")},
		{name: "read past the pack's end", method: http.MethodGet, target: blobKey, headers: rangeHeader("bytes=99999-")},
		{name: "malformed range", method: http.MethodGet, target: blobKey, headers: rangeHeader("bytes=nonsense")},

		{name: "upload an index", method: http.MethodPost, target: otherKey, body: other},
		{name: "data listing v1", method: http.MethodGet, target: "/data/", unordered: true},
		{name: "data listing v2", method: http.MethodGet, target: "/data/", headers: acceptV2(), unordered: true},
		{name: "index listing v2", method: http.MethodGet, target: "/index/", headers: acceptV2(), unordered: true},
		{name: "a listing whose trailing slash is encoded", method: http.MethodGet, target: "/data%2f", unordered: true},
		{name: "a type never written still lists", method: http.MethodGet, target: "/snapshots/"},

		{
			name: "re-upload of an existing pack", method: http.MethodPost, target: blobKey, body: payload,
			deviation: &oracleDeviation{
				reason:        "blob names are content-addressed, so a retried upload carries the same bytes; refusing it would fail a retry after a half-written object",
				wantStatus:    http.StatusOK,
				wantReference: http.StatusForbidden,
			},
		},

		{name: "missing blob", method: http.MethodHead, target: "/locks/" + strings.Repeat("f", 64)},
		{name: "get missing blob", method: http.MethodGet, target: "/locks/" + strings.Repeat("f", 64)},
		{name: "delete missing blob", method: http.MethodDelete, target: "/locks/" + strings.Repeat("f", 64)},

		{name: "delete the pack", method: http.MethodDelete, target: blobKey},
		{name: "delete the pack again", method: http.MethodDelete, target: blobKey},
		{name: "the pack is gone", method: http.MethodHead, target: blobKey},
		{name: "data listing after the delete", method: http.MethodGet, target: "/data/"},
		{name: "delete the config", method: http.MethodDelete, target: "/config"},
		{name: "delete the config again", method: http.MethodDelete, target: "/config"},

		{name: "id that is not 64 hex", method: http.MethodGet, target: "/data/" + strings.Repeat("f", 63)},
		{name: "id that is not hex at all", method: http.MethodGet, target: "/data/" + strings.Repeat("z", 64)},
		// Case matters, and this handler decides it by hand where rest-server
		// decides it with [0-9a-f]: worth asking both.
		{name: "id in upper case", method: http.MethodGet, target: "/data/" + strings.Repeat("F", 64)},
		{name: "unknown type", method: http.MethodGet, target: "/packs/" + strings.Repeat("f", 64)},
		{name: "unknown path", method: http.MethodGet, target: "/nope"},
		{name: "traversal", method: http.MethodGet, target: "/data/../config"},
		{name: "a type without its trailing slash", method: http.MethodGet, target: "/data"},
		{name: "an empty segment", method: http.MethodGet, target: "//config"},
		{name: "wrong method on an id that is not one", method: http.MethodPut, target: "/data/xyz"},
		{name: "wrong method on the root", method: http.MethodGet, target: "/"},
		{name: "wrong method on the config", method: http.MethodPut, target: "/config"},
		{name: "wrong method on a blob", method: http.MethodPut, target: blobKey},

		{
			name: "wrong method on a listing", method: http.MethodPost, target: "/data/",
			deviation: &oracleDeviation{
				reason:             "the mux serves HEAD wherever GET is registered, so a listing accepts one method more than rest-server's",
				wantStatus:         http.StatusMethodNotAllowed,
				wantReference:      http.StatusMethodNotAllowed,
				wantAllow:          []string{http.MethodGet, http.MethodHead},
				wantReferenceAllow: []string{http.MethodGet},
			},
		},
		{
			name: "head of a listing", method: http.MethodHead, target: "/data/",
			deviation: &oracleDeviation{
				reason:        "GET-implies-HEAD is mux semantics, and answering the headers of a listing is more correct HTTP than refusing them",
				wantStatus:    http.StatusOK,
				wantReference: http.StatusMethodNotAllowed,
			},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			// Act
			got := replay(t, ours, step)
			want := replay(t, reference, step)

			// Assert
			if step.deviation != nil {
				assert.Equal(t, step.deviation.wantStatus, got.Code, step.deviation.reason)
				assert.Equal(t, step.deviation.wantReference, want.Code,
					"rest-server changed its answer; the deviation may no longer be one")
				if step.deviation.wantAllow != nil {
					assert.ElementsMatch(t, step.deviation.wantAllow, allowedMethods(got), step.deviation.reason)
					assert.ElementsMatch(t, step.deviation.wantReferenceAllow, allowedMethods(want),
						"rest-server changed its Allow; the deviation may no longer be one")
				}
				return
			}
			assertSameResponse(t, want, got, step)
		})
	}
}

// The one difference the shared stream cannot show, because it starts with an
// init: rest-server's init creates the type directories, so a listing before it
// is a 404 there. This handler creates nothing — an object store has no
// directories — so "absent" and "empty" are the same state, and a fresh
// repository must still be listable.
func TestHandler_listBlobs_EmptyRepositoryDeviates(t *testing.T) {
	// Arrange
	ours, reference := newOraclePair(t)
	step := oracleStep{method: http.MethodGet, target: "/locks/"}

	// Act
	got := replay(t, ours, step)
	want := replay(t, reference, step)

	// Assert
	assert.Equal(t, http.StatusOK, got.Code)
	assert.JSONEq(t, `[]`, got.Body.String())
	assert.Equal(t, http.StatusNotFound, want.Code,
		"rest-server changed its answer; the deviation may no longer be one")
}

// newOraclePair builds this handler and rest-server's over two fresh temp dirs.
func newOraclePair(t *testing.T) (ours, reference http.Handler) {
	t.Helper()

	root := t.TempDir()
	st, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	ours = New(st)

	// The reference repo directory must not exist yet: its own POST /?create=true
	// is what creates it, and that is one of the steps under comparison.
	reference, err = repo.New(filepath.Join(t.TempDir(), "repo"), repo.Options{FsyncWarning: &sync.Once{}})
	require.NoError(t, err)
	return ours, reference
}

// replay runs one step against one handler.
func replay(t *testing.T, h http.Handler, step oracleStep) *httptest.ResponseRecorder {
	t.Helper()

	var body io.Reader
	if len(step.body) > 0 {
		body = strings.NewReader(string(step.body))
	}
	req := httptest.NewRequest(step.method, step.target, body)
	for _, kv := range step.headers {
		req.Header.Set(kv[0], kv[1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertSameResponse compares status, the meaningful headers, and the body.
func assertSameResponse(t *testing.T, want, got *httptest.ResponseRecorder, step oracleStep) {
	t.Helper()

	assert.Equal(t, want.Code, got.Code, "status")
	for _, header := range headersCompared {
		if header == "Allow" {
			assert.ElementsMatch(t, allowedMethods(want), allowedMethods(got), header)
			continue
		}
		assert.Equal(t, want.Header().Get(header), got.Header().Get(header), header)
	}
	if step.unordered {
		assert.ElementsMatch(t, jsonArray(t, want.Body.String()), jsonArray(t, got.Body.String()))
		return
	}
	assert.Equal(t, want.Body.String(), got.Body.String(), "body")
}

// allowedMethods splits an Allow header into the methods it names. RFC 9110
// gives their order no meaning, and http.ServeMux sorts where rest-server writes
// the literal "HEAD, GET, POST, DELETE", so the header is only ever compared as
// a set.
func allowedMethods(rec *httptest.ResponseRecorder) []string {
	header := rec.Header().Get("Allow")
	if header == "" {
		return nil
	}
	return strings.Split(header, ", ")
}

// jsonArray decodes a listing body so two of them can be compared as sets.
func jsonArray(t *testing.T, body string) []any {
	t.Helper()
	var parsed []any
	require.NoError(t, json.Unmarshal([]byte(body), &parsed), "body is not a JSON array: %s", body)
	return parsed
}

func acceptV2() [][2]string {
	return [][2]string{{"Accept", mimeTypeAPIV2}}
}

func rangeHeader(value string) [][2]string {
	return [][2]string{{"Range", value}}
}

// binaryPayload is a deterministic byte pattern that is not text: rest-server
// lets http.ServeContent sniff the content type, so a payload that sniffs as
// text/plain would show up as a header difference that says nothing about the
// protocol. Everything restic stores is encrypted, so binary is also the honest
// fixture.
func binaryPayload(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i % 251)
	}
	return out
}

// sha256Hex is the object name restic would give this content — rest-server
// verifies uploads against it by default, so the stream uses real names.
func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
