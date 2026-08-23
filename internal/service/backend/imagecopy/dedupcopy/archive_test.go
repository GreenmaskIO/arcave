package dedupcopy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseBlobName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHex  string
		wantBlob bool
	}{
		{
			name:     "the OCI blob path",
			input:    blobPrefix + strings.Repeat("ab", 32),
			wantHex:  strings.Repeat("ab", 32),
			wantBlob: true,
		},
		{name: "uppercase hex is not a key", input: blobPrefix + strings.Repeat("AB", 32)},
		{name: "another prefix", input: "layers/sha256/" + strings.Repeat("ab", 32)},
		{name: "a short digest", input: blobPrefix + "abcdef0123456789"},
		{name: "a path below the blob", input: blobPrefix + strings.Repeat("ab", 32) + "/data"},
		{name: "metadata", input: manifestName},
		{name: "the layout marker", input: ociLayoutName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHex, gotBlob := parseBlobName(tt.input)

			assert.Equal(t, tt.wantBlob, gotBlob)
			assert.Equal(t, tt.wantHex, gotHex)
		})
	}
}

func TestIsMetadataName(t *testing.T) {
	assert.True(t, isMetadataName(manifestName))
	assert.True(t, isMetadataName(ociLayoutName))
	assert.True(t, isMetadataName(indexName))
	assert.False(t, isMetadataName("layer.tar"))
	assert.False(t, isMetadataName("repositories"))
}

func TestValidDigest(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "lowercase hex of the right length", input: strings.Repeat("ab", 32), want: true},
		{name: "uppercase hex", input: strings.Repeat("AB", 32)},
		{name: "too short", input: strings.Repeat("ab", 31)},
		{name: "too long", input: strings.Repeat("ab", 33)},
		{name: "not hex", input: strings.Repeat("zz", 32)},
		{name: "empty", input: ""},
		{name: "a path", input: "../" + strings.Repeat("ab", 30) + "x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, validDigest(tt.input))
		})
	}
}
