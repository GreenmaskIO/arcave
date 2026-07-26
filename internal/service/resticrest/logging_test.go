package resticrest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestMain silences the global logger for the whole package: withRequestLog
// falls back to it for a request that carries no logger of its own, which is
// every request in every other file here, and a package run's worth of debug
// lines on stderr is not what a test run is for. The one test that exercises
// the fallback installs a writer of its own.
func TestMain(m *testing.M) {
	log.Logger = zerolog.Nop()
	os.Exit(m.Run())
}

// The levels are the contract, not decoration: they are what decides whether a
// line survives `log.level: info` in production. A 404 is restic asking a
// question and must stay DEBUG (a backup makes thousands of them); the answers
// nothing else on the wire explains — a 405, a path outside the grammar, a POST
// / that is not an init, an init refused — must be WARN, and a store failure
// ERROR.
func TestWithRequestLog(t *testing.T) {
	failed := fmt.Errorf("the store is unreachable")

	tests := []struct {
		name    string
		method  string
		target  string
		expect  func(repo *repositoryMock)
		status  int
		want    map[string]string // message -> level it must be logged at
		wantErr string            // substring the "error" field of the last record carries
	}{
		{
			name:   "a served request logs both ends at debug",
			method: http.MethodHead, target: "/config", status: http.StatusOK,
			expect: func(repo *repositoryMock) { repo.On("StatConfig", mock.Anything).Return(int64(12), nil) },
			want: map[string]string{
				"restic rest request":        "debug",
				"restic rest request served": "debug",
			},
		},
		{
			name:   "a method the path does not have is warned",
			method: http.MethodPut, target: "/config", status: http.StatusMethodNotAllowed,
			want: map[string]string{"restic rest request served": "warn"},
		},
		{
			name:   "a path outside the grammar is warned",
			method: http.MethodGet, target: "/nope", status: http.StatusNotFound,
			want: map[string]string{
				"path outside the restic REST grammar": "warn",
				"restic rest request served":           "debug",
			},
		},
		{
			name:   "a POST / that is not an init is warned",
			method: http.MethodPost, target: "/", status: http.StatusBadRequest,
			want: map[string]string{"POST / without create=true is not a repository init": "warn"},
		},
		{
			name:   "a missing object is debug, not warn",
			method: http.MethodHead, target: "/data/" + blobID, status: http.StatusNotFound,
			expect: func(repo *repositoryMock) {
				repo.On("StatBlob", mock.Anything, "data", blobID).Return(int64(0), errObjectNotFound)
			},
			want: map[string]string{"object not found": "debug"},
		},
		{
			name:   "an init against an existing config is warned",
			method: http.MethodPost, target: "/config", status: http.StatusForbidden,
			expect: func(repo *repositoryMock) {
				repo.On("SaveConfig", mock.Anything, mock.Anything).Return(errConfigExists)
			},
			want: map[string]string{"refusing to re-initialise a repository that already has a config": "warn"},
		},
		{
			name:   "a store failure is an error, and the log is where the cause goes",
			method: http.MethodGet, target: "/data/", status: http.StatusInternalServerError,
			expect:  func(repo *repositoryMock) { repo.On("ListBlobs", mock.Anything, "data").Return(nil, failed) },
			want:    map[string]string{"restic rest request failed": "error"},
			wantErr: "the store is unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h, repo := newMockedHandler(t)
			if tt.expect != nil {
				tt.expect(repo)
			}
			logger, buf := captureLogger()

			// Act
			rec := logged(t, h, logger, tt.method, tt.target)

			// Assert
			require.Equal(t, tt.status, rec.Code)
			records := logRecords(t, buf)
			for msg, level := range tt.want {
				assert.Equal(t, level, requireRecord(t, records, msg)["level"], "level of %q", msg)
			}
			if tt.wantErr != "" {
				assert.Contains(t, requireRecord(t, records, "restic rest request failed")["error"], tt.wantErr)
			}
		})
	}
}

// Every line carries the request it belongs to, and the completion line carries
// what nothing else can report: the status and the body size the handler wrote.
func TestWithRequestLog_RecordsWhatWasAnswered(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler(t)
	repo.On("ListBlobs", mock.Anything, "data").Return([]blob{{Name: blobID, Size: 3}}, nil)
	logger, buf := captureLogger()

	// Act
	rec := logged(t, h, logger, http.MethodGet, "/data/")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	served := requireRecord(t, logRecords(t, buf), "restic rest request served")
	assert.Equal(t, "resticrest", served["component"])
	assert.Equal(t, http.MethodGet, served["method"])
	assert.Equal(t, "/data/", served["path"])
	assert.EqualValues(t, http.StatusOK, served["status"])
	assert.EqualValues(t, rec.Body.Len(), served["bytes"])
	assert.Contains(t, served, "duration")
}

// A request that reaches the handler with no logger in its context is served
// from the global one, so a server nobody prepared a context for still says
// what it is doing.
func TestRequestLogger_FallsBackToTheGlobalLogger(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler(t)
	repo.On("StatConfig", mock.Anything).Return(int64(12), nil)
	global, buf := captureLogger()
	log.Logger = global
	t.Cleanup(func() { log.Logger = zerolog.Nop() })

	// Act
	rec := do(t, h, http.MethodHead, "/config", "")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	requireRecord(t, logRecords(t, buf), "restic rest request served")
}

// A zerolog.DefaultContextLogger is what it says: the logger for a request
// carrying none of its own, even when it is silent. This is the one case where
// detecting absence by identity and detecting it by level part company — a
// level check would overrule the silence with the global logger — so it is the
// case that pins the choice.
func TestRequestLogger_HonoursTheDefaultContextLogger(t *testing.T) {
	// Arrange
	h, repo := newMockedHandler(t)
	repo.On("StatConfig", mock.Anything).Return(int64(12), nil)
	global, buf := captureLogger()
	log.Logger = global
	silent := zerolog.Nop()
	zerolog.DefaultContextLogger = &silent
	t.Cleanup(func() {
		log.Logger = zerolog.Nop()
		zerolog.DefaultContextLogger = nil
	})

	// Act
	rec := do(t, h, http.MethodHead, "/config", "")

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, buf.String())
}

// captureLogger returns a logger writing JSON records into the buffer beside it.
func captureLogger() (zerolog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return zerolog.New(buf).Level(zerolog.DebugLevel), buf
}

// logged runs one request carrying logger in its context.
func logged(t *testing.T, h http.Handler, logger zerolog.Logger, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil).WithContext(logger.WithContext(context.Background()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// logRecords decodes what was written, one record per line.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &record), "log line: %s", line)
		records = append(records, record)
	}
	return records
}

// requireRecord returns the record carrying msg, failing the test when nothing
// logged it — which is the assertion in most of the cases above.
func requireRecord(t *testing.T, records []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, record := range records {
		if record["message"] == msg {
			return record
		}
	}
	require.Failf(t, "message was never logged", "%q not among %v", msg, records)
	return nil
}
