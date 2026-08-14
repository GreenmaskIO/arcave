package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceName_Validate(t *testing.T) {
	tests := []struct {
		name     string
		resource ResourceName
		wantErr  bool
	}{
		{name: "a plain name", resource: "mystack"},
		{name: "digits, dashes and underscores", resource: "my_stack-2"},
		{name: "empty", resource: "", wantErr: true},
		{name: "upper case", resource: "MyStack", wantErr: true},
		{name: "a nested path", resource: "a/b", wantErr: true},
		{name: "an escaping segment", resource: "..", wantErr: true},
		{name: "a leading dash", resource: "-stack", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.resource.Validate()

			// Assert
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "invalid resource name")
		})
	}
}
