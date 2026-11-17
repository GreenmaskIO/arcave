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

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addressCases is the contract the four constructors of an addressed request
// share: both segments are judged, and a request carrying half an address is
// never built. Each constructor runs the whole table, so none of them can start
// admitting what its neighbours refuse.
var addressCases = []struct {
	name       string
	objectType string
	objectName string
	wantErr    error
}{
	{name: "an object of a namespace this protocol has", objectType: "data", objectName: validID},
	{name: "a namespace this protocol does not have", objectType: "packs", objectName: validID, wantErr: ErrInvalidObjectType},
	{name: "config is not a namespace", objectType: "config", objectName: validID, wantErr: ErrInvalidObjectType},
	{name: "a name that is not an object name", objectType: "data", objectName: "nope", wantErr: ErrInvalidObjectID},
	{name: "a name in upper case", objectType: "data", objectName: strings.ToUpper(validID), wantErr: ErrInvalidObjectID},
	// The type is judged first, so a request with both segments wrong reports it.
	{name: "neither segment is one", objectType: "packs", objectName: "nope", wantErr: ErrInvalidObjectType},
}

func TestNewListBlobsRequest(t *testing.T) {
	tests := []struct {
		name       string
		objectType string
		want       ListBlobsRequest
		wantErr    bool
	}{
		{name: "a namespace this protocol has", objectType: "locks", want: ListBlobsRequest{Type: TypeLocks}},
		{name: "a namespace it does not", objectType: "packs", wantErr: true},
		{name: "the empty segment", objectType: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := NewListBlobsRequest(tt.objectType)

			// Assert
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidObjectType)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewStatBlobRequest(t *testing.T) {
	for _, tt := range addressCases {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := NewStatBlobRequest(tt.objectType, tt.objectName)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, StatBlobRequest{Type: TypeData, ID: ObjectID(validID)}, got)
		})
	}
}

func TestNewDeleteBlobRequest(t *testing.T) {
	for _, tt := range addressCases {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := NewDeleteBlobRequest(tt.objectType, tt.objectName)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, DeleteBlobRequest{Type: TypeData, ID: ObjectID(validID)}, got)
		})
	}
}

func TestNewSaveBlobRequest(t *testing.T) {
	for _, tt := range addressCases {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			body := strings.NewReader("a pack")

			// Act
			got, err := NewSaveBlobRequest(tt.objectType, tt.objectName, body)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, TypeData, got.Type)
			assert.Equal(t, ObjectID(validID), got.ID)
			// The body is carried, not read: a pack is some 16 MiB.
			assert.Same(t, body, got.Body)
			assert.Equal(t, len("a pack"), body.Len())
		})
	}
}

// TestStatBlobRequest_Read pins the only way to an OpenBlobRangeRequest: the
// address is carried over already judged, and the interval is the caller's.
func TestStatBlobRequest_ReadRange(t *testing.T) {
	tests := []struct {
		name   string
		offset int64
		length int64
	}{
		{name: "the whole object", offset: 0, length: 16},
		{name: "a chunk out of the middle", offset: 4, length: 4},
		{name: "an empty object", offset: 0, length: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			stat := StatBlobRequest{Type: TypeData, ID: ObjectID(validID)}

			// Act
			got := stat.ReadRange(tt.offset, tt.length)

			// Assert
			assert.Equal(t, OpenBlobRangeRequest{
				Type:   TypeData,
				ID:     ObjectID(validID),
				Offset: tt.offset,
				Length: tt.length,
			}, got)
		})
	}
}
