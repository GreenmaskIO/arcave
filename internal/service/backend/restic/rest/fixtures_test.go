package rest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/require"
)

// blobID is a syntactically valid object name: exactly 64 lowercase hex
// characters, which is all the protocol (and validPath) asks of one.
const blobID = "1111111111111111111111111111111111111111111111111111111111111111"

// otherBlobID is a second valid name, for the cases that need two.
const otherBlobID = "2222222222222222222222222222222222222222222222222222222222222222"

// letteredBlobID is a valid name carrying hex letters, so upper-casing it
// actually produces a different string. blobID does not: it is all digits, and a
// case test built on it passes without testing anything.
const letteredBlobID = "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"

// newStoreHandler returns the handler over a fresh directory store, plus that
// store so a test can plant objects the handler is then asked about.
func newStoreHandler(t *testing.T) (http.Handler, storages.Storager) {
	t.Helper()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	return New(store), store
}

// put plants an object directly in the store, behind the handler's back.
func put(t *testing.T, store storages.Storager, key, body string) {
	t.Helper()
	require.NoError(t, store.PutObject(context.Background(), key, strings.NewReader(body)))
}

// do runs one request against h and returns the recorded response.
func do(t *testing.T, h http.Handler, method, target, body string, headers ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for _, kv := range headers {
		req.Header.Set(kv[0], kv[1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
