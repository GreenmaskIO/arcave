package model_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

func TestContainerExitError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  model.ContainerExitError
		want string
	}{
		{
			name: "no output names only image and status",
			err:  model.ContainerExitError{Image: "busybox", StatusCode: 137},
			want: "busybox container exited with status 137",
		},
		{
			name: "output renders only the last line as a tail",
			err:  model.ContainerExitError{Image: "busybox", StatusCode: 1, Output: []string{"first", "rm: permission denied"}},
			want: "busybox container exited with status 1: rm: permission denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.err.Error()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The typed error survives %w wrapping, so a consumer up the stack recovers the
// full output with errors.As.
func TestContainerExitError_ErrorsAs(t *testing.T) {
	// Arrange
	wrapped := fmt.Errorf("restore mount: %w",
		&model.ContainerExitError{Image: "busybox", StatusCode: 2, Output: []string{"a", "b", "c"}})

	// Act
	var exitErr *model.ContainerExitError
	ok := errors.As(wrapped, &exitErr)

	// Assert
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b", "c"}, exitErr.Output)
}
