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

package encryptionmarker

import (
	"io"
	"strings"
	"testing"

	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestRepository_Get(t *testing.T) {
	tests := []struct {
		name      string
		stored    string
		want      model.EncryptionMarker
		wantFound bool
		wantErr   string
	}{
		{name: "no marker"},
		{
			name:      "a marker",
			stored:    `{"recipient":"age1recipient"}`,
			want:      model.EncryptionMarker{Recipient: "age1recipient"},
			wantFound: true,
		},
		{name: "no json", stored: `{"recip`, wantErr: "decode encryption.json"},
		{name: "no recipient", stored: `{}`, wantErr: "no recipient"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			if tt.stored != "" {
				require.NoError(t, store.PutObject(t.Context(), FileName, strings.NewReader(tt.stored)))
			}

			// Act
			got, found, err := New(store).Get(t.Context())

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantFound, found)
		})
	}
}

func TestRepository_Put(t *testing.T) {
	// Arrange
	store := inmemory.New("")
	repository := New(store)

	// Act
	err := repository.Put(t.Context(), model.EncryptionMarker{Recipient: "age1recipient"})

	// Assert
	require.NoError(t, err)
	body, err := store.GetObject(t.Context(), FileName)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, body.Close()) })
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"recipient":"age1recipient"}`, string(raw))
}
