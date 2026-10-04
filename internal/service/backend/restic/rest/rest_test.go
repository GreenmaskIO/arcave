// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// TestNewEcho is a test of the router and not of the protocol: which paths
// exist, which methods they take, and what a path that names no operation is
// answered with. It is what the generated route table is pinned by — a listing
// matching the object route, or the documentation being shadowed by a namespace,
// would show up here and nowhere else. Every path opens with the {arcaveObjectID}
// segment a client is pointed at, resolved by the double these run against.
func TestNewEcho(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		// The repository itself.
		{name: "init", method: http.MethodPost, target: "/testrepo/?create=true", wantStatus: http.StatusOK},
		{name: "a POST that is not an init", method: http.MethodPost, target: "/testrepo/", wantStatus: http.StatusBadRequest},
		{name: "removing a repository is refused", method: http.MethodDelete, target: "/testrepo/", wantStatus: http.StatusNotImplemented},

		// The config, at its own path and never as a namespace.
		{name: "stat the config", method: http.MethodHead, target: "/testrepo/config", wantStatus: http.StatusOK},
		{name: "read the config", method: http.MethodGet, target: "/testrepo/config", wantStatus: http.StatusOK},
		{name: "write the config", method: http.MethodPost, target: "/testrepo/config", wantStatus: http.StatusOK},
		{name: "remove the config", method: http.MethodDelete, target: "/testrepo/config", wantStatus: http.StatusOK},

		// A listing and an object are different routes; the trailing slash is
		// what tells them apart.
		{name: "list a namespace", method: http.MethodGet, target: "/testrepo/data/", wantStatus: http.StatusOK},
		{name: "head a listing", method: http.MethodHead, target: "/testrepo/data/", wantStatus: http.StatusOK},
		{name: "read an object", method: http.MethodGet, target: "/testrepo/data/" + string(testID), wantStatus: http.StatusOK},
		{name: "stat an object", method: http.MethodHead, target: "/testrepo/data/" + string(testID), wantStatus: http.StatusOK},
		{name: "write an object", method: http.MethodPost, target: "/testrepo/data/" + string(testID), wantStatus: http.StatusOK},
		{name: "remove an object", method: http.MethodDelete, target: "/testrepo/data/" + string(testID), wantStatus: http.StatusOK},
		{name: "every namespace is routed", method: http.MethodGet, target: "/testrepo/snapshots/", wantStatus: http.StatusOK},

		// A repository is named by any one segment, and every name that is served
		// reaches the same routes.
		{name: "another repository", method: http.MethodGet, target: "/" + otherRepoName + "/data/" + string(testID), wantStatus: http.StatusOK},
		{name: "a name of the shape a caller flattens", method: http.MethodGet, target: "/" + objectIDName + "/data/", wantStatus: http.StatusOK},

		// An unserved name is refused before any operation runs — including the
		// HEADs, which answer from their path alone.
		{name: "an object of a repository that is not served", method: http.MethodGet, target: "/" + unknownRepoName + "/data/" + string(testID), wantStatus: http.StatusNotFound},
		{name: "a listing of a repository that is not served", method: http.MethodGet, target: "/" + unknownRepoName + "/data/", wantStatus: http.StatusNotFound},
		{name: "heading a listing of a repository that is not served", method: http.MethodHead, target: "/" + unknownRepoName + "/data/", wantStatus: http.StatusNotFound},
		{name: "heading the config of a repository that is not served", method: http.MethodHead, target: "/" + unknownRepoName + "/config", wantStatus: http.StatusNotFound},
		{name: "initialising a repository that is not served", method: http.MethodPost, target: "/" + unknownRepoName + "/?create=true", wantStatus: http.StatusNotFound},

		// Everything the grammar does not admit is a flat 404 — never a
		// redirect to a cleaned path, and never a hint at the shape of a key.
		{name: "a namespace without its slash", method: http.MethodGet, target: "/testrepo/data", wantStatus: http.StatusNotFound},
		{name: "the config as a namespace", method: http.MethodGet, target: "/testrepo/config/", wantStatus: http.StatusNotFound},
		{name: "a namespace that does not exist", method: http.MethodGet, target: "/testrepo/packs/" + string(testID), wantStatus: http.StatusNotFound},
		{name: "listing a namespace that does not exist", method: http.MethodGet, target: "/testrepo/packs/", wantStatus: http.StatusNotFound},
		{name: "a name one character short", method: http.MethodGet, target: "/testrepo/data/" + string(testID)[:63], wantStatus: http.StatusNotFound},
		{name: "a traversal", method: http.MethodGet, target: "/testrepo/data/../config", wantStatus: http.StatusNotFound},
		{name: "a path no operation claims", method: http.MethodGet, target: "/testrepo/nope", wantStatus: http.StatusNotFound},

		// A path missing its repository segment is not the old rooted protocol
		// answered anyway: the first segment is always a repository name, so
		// what such a path names is a repository called "config" or "data".
		{name: "an object with no repository", method: http.MethodGet, target: "/data/" + string(testID), wantStatus: http.StatusNotFound},
		{name: "the config with no repository", method: http.MethodGet, target: "/config", wantStatus: http.StatusNotFound},
		{name: "a listing with no repository is a repository named for the namespace", method: http.MethodGet, target: "/data/", wantStatus: http.StatusMethodNotAllowed},

		// A method no operation takes keeps its own status rather than being
		// redirected to a route that would absorb it.
		{name: "a method no operation takes", method: http.MethodPut, target: "/testrepo/data/" + string(testID), wantStatus: http.StatusMethodNotAllowed},
		{name: "reading a repository's root", method: http.MethodGet, target: "/testrepo/", wantStatus: http.StatusMethodNotAllowed},
		{name: "the server's own root names no repository", method: http.MethodGet, target: "/", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			e := NewEcho(context.Background(), oneRepository(stubRepository{}))
			rec := httptest.NewRecorder()

			// Act
			e.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, strings.NewReader("")))

			// Assert
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// TestNewEcho_ResolvesTheNamedRepository is the only test that fails if
// resolution always answers with the same repository — every name in the route
// table above resolves to one instance, so it would pass either way.
func TestNewEcho_ResolvesTheNamedRepository(t *testing.T) {
	// Arrange
	first := stubRepository{body: []byte("0123456789abcdef")}
	second := stubRepository{body: []byte("fedcba9876543210")}
	e := NewEcho(context.Background(), serving(map[string]model.Repository{
		testRepoName:  first,
		otherRepoName: second,
	}))

	tests := []struct {
		name     string
		repo     string
		wantBody string
	}{
		{name: "the repository the first name resolves to", repo: testRepoName, wantBody: string(first.body)},
		{name: "the repository another name resolves to", repo: otherRepoName, wantBody: string(second.body)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			rec := httptest.NewRecorder()

			// Act
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+tt.repo+"/data/"+string(testID), nil))

			// Assert
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantBody, rec.Body.String())
		})
	}
}

// TestNewEcho_RangeReachesTheHandler pins that the header parameters the generated
// wrapper binds arrive where the handler expects them: the one place the router
// and the protocol have to agree.
func TestNewEcho_RangeReachesTheHandler(t *testing.T) {
	// Arrange
	e := NewEcho(context.Background(), oneRepository(stubRepository{}))
	req := httptest.NewRequest(http.MethodGet, "/testrepo/data/"+string(testID), nil)
	req.Header.Set("Range", "bytes=4-7")
	rec := httptest.NewRecorder()

	// Act
	e.ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 4-7/16", rec.Header().Get("Content-Range"))
	assert.Equal(t, "4567", rec.Body.String())
}

// TestNewEcho_ListingIgnoresAccept pins that the listing has one shape and that
// Accept does not choose it. The protocol's older shape — a bare array of names
// under the v1 media type — is not served, because a stock restic asks for the
// v2 one on every request it makes; the header is not read at all, so a client
// asking for something else gets the shape the Content-Type names rather than a
// refusal, which is what RFC 9110 permits.
func TestNewEcho_ListingIgnoresAccept(t *testing.T) {
	tests := []struct {
		name   string
		accept string
	}{
		{name: "no Accept"},
		{name: "the v2 media type", accept: "application/vnd.x.restic.rest.v2"},
		{name: "the v1 media type this server does not serve", accept: "application/vnd.x.restic.rest.v1"},
		{name: "anything", accept: "*/*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			e := NewEcho(context.Background(), oneRepository(stubRepository{}))
			req := httptest.NewRequest(http.MethodGet, "/testrepo/data/", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			rec := httptest.NewRecorder()

			// Act
			e.ServeHTTP(rec, req)

			// Assert
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "application/vnd.x.restic.rest.v2", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), `"size"`, "the listing is not the sized shape")
		})
	}
}

// TestNewEcho_JournalsTheCause is the whole of what the deleted error handler used
// to do, and the reason deleting it was safe: a condition no operation declares
// is a 500 whose cause is in the log and nowhere near the wire.
func TestNewEcho_JournalsTheCause(t *testing.T) {
	// Arrange
	var journal bytes.Buffer
	e := NewEcho(journalled(&journal), oneRepository(failingRepository{}))
	rec := httptest.NewRecorder()

	// Act
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/testrepo/config", nil))

	// Assert
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), errStore.Error(), "the cause reached the wire")
	assert.Contains(t, journal.String(), errStore.Error(), "the cause reached nobody")
	assert.Contains(t, journal.String(), `"level":"error"`)
}

// TestNewEcho_JournalsARecoveredPanic pins where a panic's stack goes — the logger,
// never stdout, which is a command's result channel and arcave mcp's JSON-RPC
// channel — and what the middleware order buys, which is the two assertions
// after it: Recover sits inside the journal, so the stack names the request it
// belongs to and the request is still reported answered. Both fail if the two
// are swapped, and swapping them compiles.
func TestNewEcho_JournalsARecoveredPanic(t *testing.T) {
	// Arrange
	var journal bytes.Buffer
	e := NewEcho(journalled(&journal), oneRepository(panickingRepository{}))
	rec := httptest.NewRecorder()

	// Act
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/testrepo/config", nil))

	// Assert
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	lines := strings.Split(strings.TrimSpace(journal.String()), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[1], "PANIC RECOVER")
	assert.Contains(t, lines[1], `"level":"error"`)
	assert.Contains(t, lines[1], `"component":"restic-rest"`, "the stack names no request")
	assert.Contains(t, lines[2], `"status":500`, "the request was never reported answered")
}

// TestNewEcho_JournalsBothEnds pins the two lines a request produces. One line is
// not enough: a request that never completes — a store that hangs, a ranged read
// that never returns — is invisible from a completion line alone.
func TestNewEcho_JournalsBothEnds(t *testing.T) {
	// Arrange
	var journal bytes.Buffer
	e := NewEcho(journalled(&journal), oneRepository(stubRepository{}))
	req := httptest.NewRequest(http.MethodGet, "/testrepo/data/"+string(testID), nil)
	req.Header.Set("Range", "bytes=4-7")

	// Act
	e.ServeHTTP(httptest.NewRecorder(), req)

	// Assert
	lines := strings.Split(strings.TrimSpace(journal.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], `"range":"bytes=4-7"`)
	assert.Contains(t, lines[1], `"status":206`)
	for _, line := range lines {
		assert.Contains(t, line, `"component":"restic-rest"`)
	}
}

// TestNewEcho_JournalsServedRequestsAtDebug pins that a request answered is
// silent at INFO — restic issues one per blob — while a 500 is not.
func TestNewEcho_JournalsServedRequestsAtDebug(t *testing.T) {
	tests := []struct {
		name      string
		repo      model.Repository
		wantEmpty bool
	}{
		{name: "served", repo: stubRepository{}, wantEmpty: true},
		{name: "failed", repo: failingRepository{}, wantEmpty: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var journal bytes.Buffer
			ctx := zerolog.New(&journal).Level(zerolog.InfoLevel).WithContext(context.Background())
			e := NewEcho(ctx, oneRepository(tt.repo))

			// Act
			e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/testrepo/config", nil))

			// Assert
			assert.Equal(t, tt.wantEmpty, journal.Len() == 0, journal.String())
		})
	}
}

// TestNewEcho_Docs pins that the documentation is reachable, and what reaching
// it costs: echo prefers a static segment to a parametric one, so "docs" wins
// against {arcaveObjectID} and no repository is addressable under that name. The last
// case is that price, and it is what would change silently if the routes were
// ever mounted the other way round.
func TestNewEcho_Docs(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantBody string
	}{
		{name: "the ui", target: "/docs/index.html", wantBody: "swagger"},
		{name: "the document", target: "/docs/doc.json", wantBody: `"openapi"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			e := NewEcho(context.Background(), oneRepository(stubRepository{}))
			rec := httptest.NewRecorder()

			// Act
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))

			// Assert
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, strings.ToLower(rec.Body.String()), strings.ToLower(tt.wantBody))
		})
	}
}

// TestNewEcho_JournalsWhatEscapesTheChain pins where net/http's own reports go —
// a panic past the recover, a malformed request line, a connection that dies
// mid-header. StdLogger is what echo hands net/http as its ErrorLog when it
// starts serving, and echo.New aims it at stdout, which is a command's result
// channel and arcave mcp's JSON-RPC channel.
func TestNewEcho_JournalsWhatEscapesTheChain(t *testing.T) {
	// Arrange
	var journal bytes.Buffer
	e := NewEcho(journalled(&journal), oneRepository(stubRepository{}))

	// Act
	e.StdLogger.Print("http: panic serving 127.0.0.1:1234")

	// Assert
	assert.Contains(t, journal.String(), "http: panic serving")
	assert.Contains(t, journal.String(), `"component":"restic-rest"`)
}

// TestNewEcho_BaseContextCarriesTheCommandsLogger pins what every request is
// derived from. Without it a layer under the routes would fall back to zerolog's
// global logger — level and format unresolved — and nothing would say so.
func TestNewEcho_BaseContextCarriesTheCommandsLogger(t *testing.T) {
	// Arrange
	var journal bytes.Buffer
	e := NewEcho(journalled(&journal), oneRepository(stubRepository{}))
	require.NotNil(t, e.Server.BaseContext)

	// Act
	zerolog.Ctx(e.Server.BaseContext(nil)).Info().Msg("from the command's logger")

	// Assert
	assert.Contains(t, journal.String(), "from the command's logger")
}

// TestNewEcho_DocsShadowsARepositoryOfThatName states that preference as
// behaviour: a listing addressed to a repository named "docs" is answered by the
// documentation and never reaches the repository. Nothing has to guard against
// it — every arcave ObjectID opens with "volume-", so no repository is ever
// named "docs" — but the shadowing is real and worth saying out loud.
func TestNewEcho_DocsShadowsARepositoryOfThatName(t *testing.T) {
	// Arrange
	e := NewEcho(context.Background(), oneRepository(stubRepository{}))
	rec := httptest.NewRecorder()

	// Act
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/data/", nil))

	// Assert
	assert.NotEqual(t, "application/vnd.x.restic.rest.v2", rec.Header().Get("Content-Type"),
		"the listing reached the repository")
}
