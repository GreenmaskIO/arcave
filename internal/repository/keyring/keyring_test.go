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

package keyring

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// testWorkFactor keeps scrypt to milliseconds.
const testWorkFactor = 10

const testPassword = "correct horse"

func TestRepository_SaveKey(t *testing.T) {
	// Arrange
	store := inmemory.New("")
	repo := New(store).SetWorkFactor(testWorkFactor)
	key := newTestKey(t)

	// Act
	err := repo.SaveKey(t.Context(), key, testPassword)

	// Assert
	require.NoError(t, err)
	raw := readObject(t, store, key.ID.String()+".age")
	assert.NotContains(t, string(raw), key.Identity, "the identity is stored wrapped")
	got, err := repo.LoadKey(t.Context(), key.ID, testPassword)
	require.NoError(t, err)
	assert.Equal(t, key, got)
}

func TestRepository_LoadKey(t *testing.T) {
	key := newTestKey(t)
	other := newTestKey(t)
	tests := []struct {
		name     string
		id       model.StoreKeyID
		stored   model.StoreKey
		password string
		wantErr  error
		wantMsg  string
	}{
		{name: "the right password", id: key.ID, stored: key, password: testPassword},
		{name: "a wrong password", id: key.ID, stored: key, password: "wrong", wantErr: model.ErrWrongPassword},
		{name: "a key filed under another's id", id: other.ID, stored: key, password: testPassword, wantMsg: "names itself"},
		{name: "no such key", id: other.ID, password: testPassword, wantErr: storages.ErrFileNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			repo := New(store).SetWorkFactor(testWorkFactor)
			if tt.stored.ID != "" {
				putKey(t, store, tt.id, tt.stored)
			}

			// Act
			got, err := repo.LoadKey(t.Context(), tt.id, tt.password)

			// Assert
			switch {
			case tt.wantErr != nil:
				assert.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				assert.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.stored, got)
			}
		})
	}
}

func TestRepository_ListKeys(t *testing.T) {
	// Arrange
	store := inmemory.New("")
	for _, name := range []string{"ffffffffffffffff.age", "0000000000000001.age", "README", "0000000000000002.txt", "XYZ.age"} {
		require.NoError(t, store.PutObject(t.Context(), name, strings.NewReader("x")))
	}

	// Act
	ids, err := New(store).ListKeys(t.Context())

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.StoreKeyID{"0000000000000001", "ffffffffffffffff"}, ids)
}

func TestRepository_DeleteKey(t *testing.T) {
	// Arrange
	store := inmemory.New("")
	repo := New(store).SetWorkFactor(testWorkFactor)
	key := newTestKey(t)
	require.NoError(t, repo.SaveKey(t.Context(), key, testPassword))

	// Act
	err := repo.DeleteKey(t.Context(), key.ID)

	// Assert
	require.NoError(t, err)
	ids, err := repo.ListKeys(t.Context())
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func newTestKey(t *testing.T) model.StoreKey {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return model.StoreKey{
		Version:      model.StoreKeyVersion,
		ID:           model.NewStoreKeyID(identity.Recipient().String()),
		CreatedAt:    time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC),
		Identity:     identity.String(),
		ResticSecret: bytes.Repeat([]byte{7}, 32),
	}
}

// putKey stores key under id, whatever id the key names itself.
func putKey(t *testing.T, store storages.Storager, id model.StoreKeyID, key model.StoreKey) {
	t.Helper()
	recipient, err := age.NewScryptRecipient(testPassword)
	require.NoError(t, err)
	recipient.SetWorkFactor(testWorkFactor)
	data, err := json.Marshal(key)
	require.NoError(t, err)
	ciphertext, err := age.EncryptReader(bytes.NewReader(data), recipient)
	require.NoError(t, err)
	require.NoError(t, store.PutObject(t.Context(), id.String()+".age", ciphertext))
}

func readObject(t *testing.T, store storages.Storager, key string) []byte {
	t.Helper()
	body, err := store.GetObject(t.Context(), key)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, body.Close()) })
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	return data
}
