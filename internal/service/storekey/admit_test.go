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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
	"github.com/greenmaskio/arcave/internal/utils"
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

func TestPlainAdmission_AdmitWrite(t *testing.T) {
	tests := []struct {
		name    string
		marker  *model.EncryptionMarker
		wantErr error
	}{
		{name: "an unmarked project"},
		{name: "a project an encrypted host marked since", marker: &model.EncryptionMarker{Recipient: "age1other"}, wantErr: model.ErrStoreEncrypted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: no Put expectation, so a write fails the test.
			markers := &markersMock{}
			markers.On("Get", mock.Anything).Return(tt.marker, nil).Once()

			// Act
			err := NewPlainAdmission(markers).AdmitWrite(t.Context())

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			markers.AssertExpectations(t)
		})
	}
}

// arcaveProjectContentsMock doubles arcaveProjectContents.
type arcaveProjectContentsMock struct {
	mock.Mock
}

func (m *arcaveProjectContentsMock) HoldsData(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

// markersMock doubles markerRepository; Get returns the *model.EncryptionMarker it is handed, nil
// for none.
type markersMock struct {
	mock.Mock
}

func (m *markersMock) Get(ctx context.Context) (model.EncryptionMarker, bool, error) {
	args := m.Called(ctx)
	if err := args.Error(1); err != nil {
		return model.EncryptionMarker{}, false, err
	}
	marker, _ := args.Get(0).(*model.EncryptionMarker)
	if marker == nil {
		return model.EncryptionMarker{}, false, nil
	}
	return *marker, true, nil
}

func (m *markersMock) Put(ctx context.Context, marker model.EncryptionMarker) error {
	return m.Called(ctx, marker).Error(0)
}

var errStore = errors.New("store unreachable")

func TestRequireEncrypted(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ours := identity.Recipient().String()
	tests := []struct {
		name      string
		marker    *model.EncryptionMarker
		getErr    error
		holdsData *bool
		holdsErr  error
		wantErr   error
		wantMsg   string
	}{
		{name: "marked for the key", marker: &model.EncryptionMarker{Recipient: ours}},
		{
			name:    "marked for another key",
			marker:  &model.EncryptionMarker{Recipient: "age1other"},
			wantErr: model.ErrWrongStoreKey,
			wantMsg: "age1other",
		},
		{name: "unmarked and empty stays unmarked", holdsData: utils.New(false)},
		{name: "unmarked and holding data", holdsData: utils.New(true), wantErr: model.ErrStoreHoldsPlainData},
		{name: "the marker unreadable", getErr: errStore, wantErr: errStore},
		{name: "the listing failing", holdsData: utils.New(false), holdsErr: errStore, wantErr: errStore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: no Put expectation, so a write fails the test.
			markers := &markersMock{}
			contents := &arcaveProjectContentsMock{}
			calls := []*mock.Call{markers.On("Get", mock.Anything).Return(tt.marker, tt.getErr).Once()}
			if tt.holdsData != nil {
				calls = append(calls, contents.On("HoldsData", mock.Anything).Return(*tt.holdsData, tt.holdsErr).Once())
			}
			mock.InOrder(calls...)

			// Act
			err := RequireEncrypted(t.Context(), markers, contents, identity)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMsg)
			} else {
				require.NoError(t, err)
			}
			markers.AssertExpectations(t)
			contents.AssertExpectations(t)
		})
	}
}

func TestEncryptedAdmission_AdmitWrite(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ours := identity.Recipient().String()
	tests := []struct {
		name      string
		marker    *model.EncryptionMarker
		getErr    error
		holdsData *bool
		holdsErr  error
		// put expects the marker for the key written, failing with putErr.
		put     bool
		putErr  error
		wantErr error
		wantMsg string
	}{
		{name: "marked for the key is not marked again", marker: &model.EncryptionMarker{Recipient: ours}},
		{
			name:    "marked for another key",
			marker:  &model.EncryptionMarker{Recipient: "age1other"},
			wantErr: model.ErrWrongStoreKey,
			wantMsg: "age1other",
		},
		{name: "unmarked and empty is marked", holdsData: utils.New(false), put: true},
		{name: "unmarked and holding data", holdsData: utils.New(true), wantErr: model.ErrStoreHoldsPlainData},
		{name: "the marker unreadable", getErr: errStore, wantErr: errStore},
		{name: "the listing failing", holdsData: utils.New(false), holdsErr: errStore, wantErr: errStore},
		{name: "the marker unwritable", holdsData: utils.New(false), put: true, putErr: errStore, wantErr: errStore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			markers := &markersMock{}
			contents := &arcaveProjectContentsMock{}
			calls := []*mock.Call{markers.On("Get", mock.Anything).Return(tt.marker, tt.getErr).Once()}
			if tt.holdsData != nil {
				calls = append(calls, contents.On("HoldsData", mock.Anything).Return(*tt.holdsData, tt.holdsErr).Once())
			}
			if tt.put {
				calls = append(calls, markers.On("Put", mock.Anything, model.EncryptionMarker{Recipient: ours}).Return(tt.putErr).Once())
			}
			mock.InOrder(calls...)

			// Act
			err := NewEncryptedAdmission(markers, contents, identity).AdmitWrite(t.Context())

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMsg)
			} else {
				require.NoError(t, err)
			}
			markers.AssertExpectations(t)
			contents.AssertExpectations(t)
		})
	}
}
