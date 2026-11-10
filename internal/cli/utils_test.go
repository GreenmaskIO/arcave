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

package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	errDanglingPointer = fmt.Errorf("%w: 019e0000-0000-7000-8000-000000000002, named by .arcave/current_session",
		model.ErrCurrentSessionNotFound)
	errUnreadablePointer = fmt.Errorf("%w: decoding .arcave/current_session: %w",
		model.ErrCurrentSessionUnreadable, model.ErrInvalidSessionID)
	errCurrentDelete = fmt.Errorf("%w: 019e0000-0000-7000-8000-000000000002", model.ErrCurrentSessionDelete)
	errUnrelated     = errors.New("store unreadable")
)

func TestWithFixHint(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantText string
	}{
		{
			name:     "a dangling pointer",
			err:      errDanglingPointer,
			wantText: errDanglingPointer.Error() + "; run 'arcave session unset' or 'arcave session set <session>'",
		},
		{
			name:     "an unreadable pointer",
			err:      errUnreadablePointer,
			wantText: errUnreadablePointer.Error() + "; run 'arcave session unset' or 'arcave session set <session>'",
		},
		{
			name: "deleting the current session",
			err:  errCurrentDelete,
			wantText: errCurrentDelete.Error() + "; switch with 'arcave session set <session>' or " +
				"'arcave session unset' first, or pass --force to delete it and clear the pointer",
		},
		{name: "an unrelated error", err: errUnrelated, wantText: errUnrelated.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := WithFixHint(tt.err)

			// Assert
			require.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.wantText, got.Error())
		})
	}
}

func TestWithFixHint_Nil(t *testing.T) {
	// Act & Assert
	assert.NoError(t, WithFixHint(nil))
}

func TestMCPError_CurrentSession(t *testing.T) {
	pin := model.DefaultSession
	tests := []struct {
		name     string
		err      error
		pinned   model.SessionID
		wantText string
	}{
		{name: "a dangling pointer", err: errDanglingPointer,
			wantText: errDanglingPointer.Error() + "; call session_unset or session_set"},
		{name: "an unreadable pointer", err: errUnreadablePointer,
			wantText: errUnreadablePointer.Error() + "; call session_unset or session_set"},
		{name: "deleting the session in use", err: errCurrentDelete,
			wantText: errCurrentDelete.Error() + "; switch with session_set or session_unset first, or pass force " +
				"to delete it anyway"},
		{name: "deleting the session in use on a pinned server", err: errCurrentDelete, pinned: pin,
			wantText: errCurrentDelete.Error() + "; pass force to delete it anyway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			handler := &MCPHandler{pinnedSession: tt.pinned}

			// Act
			got := handler.mcpError(tt.err)

			// Assert
			require.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.wantText, got.Error())
			assert.NotContains(t, got.Error(), "arcave session")
		})
	}
}
