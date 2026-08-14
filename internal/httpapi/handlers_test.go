package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The served surface is the spec's: the declared path answers, everything else
// is refused.
func TestNewHTTPHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{
			name:   "the healthcheck answers",
			method: http.MethodGet, target: "/api/v1/healthcheck", wantStatus: http.StatusOK,
		},
		{
			name:   "a path the spec does not declare is not served",
			method: http.MethodGet, target: "/api/v1/nope", wantStatus: http.StatusNotFound,
		},
		{
			name:   "the root is not served either",
			method: http.MethodGet, target: "/", wantStatus: http.StatusNotFound,
		},
		{
			// The route table knows the path's methods, so this is a 405, not a 404.
			name:   "a method the path does not declare is refused",
			method: http.MethodPost, target: "/api/v1/healthcheck", wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := NewHTTPHandler("1.2.3")
			rec := httptest.NewRecorder()

			// Act
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))

			// Assert
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// The body is the contract the CLI reads, so both its fields are asserted.
func TestHandlers_Healthcheck(t *testing.T) {
	// Arrange
	h := NewHTTPHandler("1.2.3")
	rec := httptest.NewRecorder()

	// Act
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/healthcheck", nil))

	// Assert
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var got HealthcheckResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, HealthcheckResponse{Status: statusOK, Version: "1.2.3"}, got)
}
