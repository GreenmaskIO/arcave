package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRawSettings_Decode(t *testing.T) {
	type nested struct {
		Path string `mapstructure:"path"`
	}
	type target struct {
		Host    string        `mapstructure:"host"`
		Timeout time.Duration `mapstructure:"timeout"`
		Files   []string      `mapstructure:"files"`
		Nested  nested        `mapstructure:"nested"`
	}

	tests := []struct {
		name    string
		raw     RawSettings
		want    target
		wantErr string
	}{
		{
			name: "every section reads",
			raw: RawSettings{
				"host":    "unix:///docker.sock",
				"timeout": "120s",
				"files":   []any{"a.yaml", "b.yaml"},
				"nested":  map[string]any{"path": "/data"},
			},
			want: target{
				Host:    "unix:///docker.sock",
				Timeout: 120 * time.Second,
				Files:   []string{"a.yaml", "b.yaml"},
				Nested:  nested{Path: "/data"},
			},
		},
		{
			name: "a duration written as a string is converted",
			raw:  RawSettings{"timeout": "1m30s"},
			want: target{Timeout: 90 * time.Second},
		},
		{
			name: "a comma-separated list becomes a slice",
			raw:  RawSettings{"files": "a.yaml,b.yaml"},
			want: target{Files: []string{"a.yaml", "b.yaml"}},
		},
		{
			name: "no settings at all is not an error",
			raw:  nil,
			want: target{},
		},
		{
			name:    "an unknown key is a typo",
			raw:     RawSettings{"hsot": "unix:///docker.sock"},
			wantErr: "hsot",
		},
		{
			name:    "an unknown key inside a section is a typo too",
			raw:     RawSettings{"nested": map[string]any{"paht": "/data"}},
			wantErr: "paht",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var got target

			// Act
			err := tt.raw.Decode(&got)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
