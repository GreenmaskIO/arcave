package model_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const validDigest = "sha256:5f2b8d7c1a4e6039bc82d1f470a3e59c8d24b7061fe93a5c8b70d1e2f4a6c983"

func TestParseDigestID(t *testing.T) {
	// Act
	id, err := model.ParseImageDigestID(validDigest)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, validDigest, id.String())
}

func TestParseDigestID_RefusesWhatIsNotContent(t *testing.T) {
	tests := []struct {
		name string
		s    string
	}{
		{name: "a tag", s: "postgres:16"},
		{name: "a tag a rollback left behind", s: "arcave/demo-db-1:sp-1780335122"},
		{name: "an untagged repository", s: "postgres"},
		{name: "a truncated id", s: "sha256:5f2b8d7c1a4e"},
		{name: "hex with no algorithm", s: validDigest[len("sha256:"):]},
		{name: "another algorithm", s: "sha512:" + validDigest[len("sha256:"):]},
		{name: "upper-case hex", s: "sha256:5F2B8D7C1A4E6039BC82D1F470A3E59C8D24B7061FE93A5C8B70D1E2F4A6C983"},
		{name: "a digit too many", s: validDigest + "0"},
		{name: "empty", s: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := model.ParseImageDigestID(tt.s)

			// Assert
			require.ErrorIs(t, err, model.ErrInvalidDigestID)
		})
	}
}

func TestDigestID_UnmarshalJSON(t *testing.T) {
	// Arrange
	var capture model.ImageCapture

	// Act
	err := json.Unmarshal([]byte(`{"image_id":"`+validDigest+`"}`), &capture)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ImageDigestID(validDigest), capture.ImageID)
}

func TestDigestID_UnmarshalJSON_RefusesAReference(t *testing.T) {
	// Arrange
	var capture model.ImageCapture

	// Act
	err := json.Unmarshal([]byte(`{"image_id":"postgres:16"}`), &capture)

	// Assert: a stored id that is not content fails at the decode, not at the daemon.
	require.ErrorIs(t, err, model.ErrInvalidDigestID)
}

func TestDigestID_MarshalJSON_RoundTrips(t *testing.T) {
	// Arrange
	capture := model.ImageCapture{ImageID: model.ImageDigestID(validDigest)}

	// Act
	body, err := json.Marshal(capture)

	// Assert
	require.NoError(t, err)
	assert.Contains(t, string(body), `"image_id":"`+validDigest+`"`)
}
