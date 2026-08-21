package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStackName_Validate(t *testing.T) {
	tests := []struct {
		name    string
		stack   StackName
		wantErr bool
	}{
		{name: "a plain name", stack: "mystack"},
		{name: "digits, dashes and underscores", stack: "my_stack-2"},
		{name: "empty", stack: "", wantErr: true},
		{name: "upper case", stack: "MyStack", wantErr: true},
		{name: "a nested path", stack: "a/b", wantErr: true},
		{name: "an escaping segment", stack: "..", wantErr: true},
		{name: "a leading dash", stack: "-stack", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.stack.Validate()

			// Assert
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "invalid stack name")
		})
	}
}
