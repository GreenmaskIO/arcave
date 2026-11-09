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

	"filippo.io/age"
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
			admitErr := NewPlainAdmission(encryptionmarker.New(store)).AdmitWrite(t.Context())

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, admitErr, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, admitErr)
		})
	}
}

// projectContentsMock is the arcave project's listing.
type projectContentsMock struct {
	mock.Mock
}

func (m *projectContentsMock) HoldsData(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

// failingMarkers is a marker repository whose every call fails.
type failingMarkers struct {
	getErr error
	putErr error
	put    bool
}

func (m *failingMarkers) Get(context.Context) (model.EncryptionMarker, bool, error) {
	return model.EncryptionMarker{}, false, m.getErr
}

func (m *failingMarkers) Put(context.Context, model.EncryptionMarker) error {
	m.put = true
	return m.putErr
}

// admitWrite is EncryptedAdmission.AdmitWrite in the shape of RequireEncrypted.
func admitWrite(ctx context.Context, markers markerRepository, contents projectContents, identity *age.X25519Identity) error {
	return NewEncryptedAdmission(markers, contents, identity).AdmitWrite(ctx)
}

func TestAdmission(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ours := identity.Recipient().String()
	type admission func(context.Context, markerRepository, projectContents, *age.X25519Identity) error
	admissions := []struct {
		name   string
		admit  admission
		writes bool
	}{
		{name: "reader", admit: RequireEncrypted},
		{name: "writer", admit: admitWrite, writes: true},
	}
	tests := []struct {
		name      string
		marker    string
		holdsData *bool
		wantErr   error
		// wantMarker is the recipient left stored, by a writer when it differs.
		wantMarker       string
		wantWriterMarker string
	}{
		{
			name:       "marked for the key",
			marker:     `{"recipient":"` + ours + `"}`,
			wantMarker: ours,
		},
		{
			name:       "marked for another key",
			marker:     `{"recipient":"age1other"}`,
			wantErr:    model.ErrWrongStoreKey,
			wantMarker: "age1other",
		},
		{
			name:             "unmarked and empty",
			holdsData:        utils.New(false),
			wantWriterMarker: ours,
		},
		{
			name:      "unmarked and holding data",
			holdsData: utils.New(true),
			wantErr:   model.ErrStoreHoldsPlainData,
		},
	}
	for _, adm := range admissions {
		for _, tt := range tests {
			t.Run(adm.name+"/"+tt.name, func(t *testing.T) {
				// Arrange
				store := inmemory.New("")
				if tt.marker != "" {
					require.NoError(t, store.PutObject(t.Context(), encryptionmarker.FileName, strings.NewReader(tt.marker)))
				}
				contents := &projectContentsMock{}
				if tt.holdsData != nil {
					contents.On("HoldsData", mock.Anything).Return(*tt.holdsData, nil).Once()
				}
				wantMarker := tt.wantMarker
				if adm.writes && tt.wantWriterMarker != "" {
					wantMarker = tt.wantWriterMarker
				}

				// Act
				err := adm.admit(t.Context(), encryptionmarker.New(store), contents, identity)

				// Assert
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				} else {
					require.NoError(t, err)
				}
				marker, found, err := encryptionmarker.New(store).Get(t.Context())
				require.NoError(t, err)
				assert.Equal(t, wantMarker != "", found)
				assert.Equal(t, wantMarker, marker.Recipient)
				contents.AssertExpectations(t)
			})
		}
	}
}

func TestAdmission_Failures(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	errStore := errors.New("store unreachable")
	type admission func(context.Context, markerRepository, projectContents, *age.X25519Identity) error
	tests := []struct {
		name      string
		admit     admission
		markers   *failingMarkers
		holdsErr  error
		wantHolds bool
		wantPut   bool
	}{
		{name: "reader, the marker unreadable", admit: RequireEncrypted, markers: &failingMarkers{getErr: errStore}},
		{name: "writer, the marker unreadable", admit: admitWrite, markers: &failingMarkers{getErr: errStore}},
		{name: "reader, the listing failing", admit: RequireEncrypted, markers: &failingMarkers{}, holdsErr: errStore, wantHolds: true},
		{name: "writer, the listing failing", admit: admitWrite, markers: &failingMarkers{}, holdsErr: errStore, wantHolds: true},
		{name: "writer, the marker unwritable", admit: admitWrite, markers: &failingMarkers{putErr: errStore}, wantHolds: true, wantPut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			contents := &projectContentsMock{}
			if tt.wantHolds {
				contents.On("HoldsData", mock.Anything).Return(false, tt.holdsErr).Once()
			}

			// Act
			err := tt.admit(t.Context(), tt.markers, contents, identity)

			// Assert
			require.ErrorIs(t, err, errStore)
			assert.Equal(t, tt.wantPut, tt.markers.put)
			contents.AssertExpectations(t)
		})
	}
}
