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
	"strings"
	"testing"

	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
)

func TestRequirePlain(t *testing.T) {
	tests := []struct {
		name    string
		marker  string
		wantErr error
	}{
		{name: "no marker"},
		{name: "a marker", marker: `{"recipient":"age1other"}`, wantErr: model.ErrStoreEncrypted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			if tt.marker != "" {
				require.NoError(t, store.PutObject(t.Context(), encryptionmarker.FileName, strings.NewReader(tt.marker)))
			}

			// Act
			err := RequirePlain(t.Context(), encryptionmarker.New(store))

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAdmitEncrypted(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ours := identity.Recipient().String()
	tests := []struct {
		name       string
		marker     string
		beside     string
		wantErr    error
		wantMarker string
	}{
		{name: "an empty project is marked", wantMarker: ours},
		{name: "a project with objects but no marker is marked", beside: "sessions/x/session.json", wantMarker: ours},
		{name: "a project marked for the key", marker: `{"recipient":"` + ours + `"}`, wantMarker: ours},
		{
			name:       "a project marked for another key",
			marker:     `{"recipient":"age1other"}`,
			wantErr:    model.ErrWrongStoreKey,
			wantMarker: "age1other",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			if tt.marker != "" {
				require.NoError(t, store.PutObject(t.Context(), encryptionmarker.FileName, strings.NewReader(tt.marker)))
			}
			if tt.beside != "" {
				require.NoError(t, store.PutObject(t.Context(), tt.beside, strings.NewReader("{}")))
			}

			// Act
			err := AdmitEncrypted(t.Context(), encryptionmarker.New(store), identity)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMarker)
			} else {
				require.NoError(t, err)
			}
			marker, found, err := encryptionmarker.New(store).Get(t.Context())
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, tt.wantMarker, marker.Recipient)
		})
	}
}

func TestRequireKey(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ours := identity.Recipient().String()
	tests := []struct {
		name       string
		marker     string
		wantErr    error
		wantMarker string
	}{
		{name: "an unmarked project stays unmarked"},
		{name: "a project marked for the key", marker: `{"recipient":"` + ours + `"}`, wantMarker: ours},
		{
			name:       "a project marked for another key",
			marker:     `{"recipient":"age1other"}`,
			wantErr:    model.ErrWrongStoreKey,
			wantMarker: "age1other",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			if tt.marker != "" {
				require.NoError(t, store.PutObject(t.Context(), encryptionmarker.FileName, strings.NewReader(tt.marker)))
			}

			// Act
			err := RequireKey(t.Context(), encryptionmarker.New(store), identity)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMarker)
			} else {
				require.NoError(t, err)
			}
			marker, found, err := encryptionmarker.New(store).Get(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.marker != "", found)
			assert.Equal(t, tt.wantMarker, marker.Recipient)
		})
	}
}
