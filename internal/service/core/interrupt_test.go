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

package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestPreferInterrupt(t *testing.T) {
	tests := []struct {
		name    string
		cause   error
		wantErr error
	}{
		{name: "a running workflow keeps its own failure", wantErr: errWorkflow},
		{name: "a Ctrl-C reports the interrupt", cause: context.Canceled, wantErr: context.Canceled},
		{name: "a lost session lock reports the lost lock", cause: model.ErrSessionLockLost, wantErr: model.ErrSessionLockLost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancelCause(newLockTestContext())
			defer cancel(nil)
			if tt.cause != nil {
				cancel(tt.cause)
			}

			// Act
			err := preferInterrupt(ctx, errWorkflow)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
