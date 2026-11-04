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

package storekey

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"filippo.io/age"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestParseKey(t *testing.T) {
	const malformed = "AGE-SECRET-KEY-1NOTAKEY"
	tests := []struct {
		name    string
		key     string
		wantErr error
		wantMsg string
	}{
		{name: "a generated key", key: newTestKey(t)},
		{name: "a malformed key", key: malformed, wantErr: model.ErrInvalidStoreKey},
		{name: "no key", key: "", wantErr: model.ErrInvalidStoreKey, wantMsg: "none set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			identity, err := ParseKey(tt.key)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMsg)
				assert.NotContains(t, err.Error(), malformed, "the key stays out of the error")
				assert.Nil(t, identity)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.key, identity.String())
		})
	}
}

func TestNewResticPassword(t *testing.T) {
	// Arrange
	key := newTestKey(t)
	identity, err := ParseKey(key)
	require.NoError(t, err)
	other, err := ParseKey(newTestKey(t))
	require.NoError(t, err)

	// Act
	password, err := NewResticPassword(identity)
	again, againErr := NewResticPassword(identity)
	otherPassword, otherErr := NewResticPassword(other)

	// Assert
	require.NoError(t, err)
	require.NoError(t, againErr)
	require.NoError(t, otherErr)
	assert.Equal(t, password, again, "the password is derived, not drawn")
	assert.NotEqual(t, password, otherPassword)
	assert.NotContains(t, password, key)
	assert.Len(t, password, 2*resticPasswordSize)
}

func newTestKey(t *testing.T) string {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return identity.String()
}
