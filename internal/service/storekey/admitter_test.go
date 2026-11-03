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
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
)

const tornMarker = `{"version":1,"recip`

func TestOpener_RequirePlain(t *testing.T) {
	tests := []struct {
		name    string
		marker  string
		wantErr error
	}{
		{name: "no marker"},
		{name: "a marker", marker: `{"version":1,"recipient":"age1other"}`, wantErr: model.ErrStoreEncrypted},
		{name: "a torn marker", marker: tornMarker, wantErr: model.ErrInvalidEncryptionMarker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			if tt.marker != "" {
				require.NoError(t, store.PutObject(t.Context(), encryptionmarker.FileName, strings.NewReader(tt.marker)))
			}

			// Act
			err := NewAdmitter().RequirePlain(t.Context(), encryptionmarker.New(store))

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestOpener_AdmitEncrypted(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ours := identity.Recipient().String()
	tests := []struct {
		name       string
		marker     string
		data       bool
		wantErr    error
		wantMarker string
	}{
		{name: "an empty project is marked", wantMarker: ours},
		{name: "a project marked for the key", marker: `{"version":1,"recipient":"` + ours + `"}`, wantMarker: ours},
		{
			name:       "a project marked for another key",
			marker:     `{"version":1,"recipient":"age1other"}`,
			data:       true,
			wantErr:    model.ErrWrongStoreKey,
			wantMarker: "age1other",
		},
		{name: "unmarked data", data: true, wantErr: model.ErrStoreUnencrypted},
		{name: "a torn marker over an empty project is written again", marker: tornMarker, wantMarker: ours},
		{name: "a torn marker over data", marker: tornMarker, data: true, wantErr: model.ErrInvalidEncryptionMarker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("")
			if tt.marker != "" {
				require.NoError(t, store.PutObject(t.Context(), encryptionmarker.FileName, strings.NewReader(tt.marker)))
			}
			if tt.data {
				require.NoError(t, store.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("{}")))
			}

			// Act
			err := NewAdmitter().SetSettle(0).AdmitEncrypted(t.Context(), store, encryptionmarker.New(store), identity)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.wantMarker == "" {
				assert.Equal(t, tt.marker, readMarkerObject(t, store), "a refusal leaves the marker as it was")
				return
			}
			marker, found, err := encryptionmarker.New(store).Get(t.Context())
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, tt.wantMarker, marker.Recipient)
		})
	}
}

// What the marker repository returns, or lands after the settle, decides the admission.
func TestAdmitter_AdmitEncrypted_MarkerRepository(t *testing.T) {
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	errStore := errors.New("store down")
	tests := []struct {
		name    string
		markers *markersStub
		wantErr error
	}{
		{name: "a store that cannot read the marker", markers: &markersStub{getErr: errStore}, wantErr: errStore},
		{name: "a store that cannot write the marker", markers: &markersStub{putErr: errStore}, wantErr: errStore},
		{
			name:    "a marker torn after the settle",
			markers: &markersStub{afterPut: fmt.Errorf("%w: torn", model.ErrInvalidEncryptionMarker)},
			wantErr: model.ErrInvalidEncryptionMarker,
		},
		{name: "a marker gone after the settle", markers: &markersStub{gone: true}, wantErr: errMarkerGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := NewAdmitter().SetSettle(0).AdmitEncrypted(t.Context(), inmemory.New(""), tt.markers, identity)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// A context cancelled during the settle ends the admission with its cause.
func TestAdmitter_AdmitEncrypted_CancelledDuringSettle(t *testing.T) {
	// Arrange
	identity, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("lock lost")
	cancel(cause)

	// Act: the stub writes whatever the context says, so the cancellation lands at the settle.
	err = NewAdmitter().SetSettle(time.Hour).AdmitEncrypted(ctx, inmemory.New(""), &markersStub{}, identity)

	// Assert
	require.ErrorIs(t, err, cause)
}

// markersStub is a marker repository failing where it is told to.
type markersStub struct {
	marker   model.EncryptionMarker
	written  bool
	getErr   error
	putErr   error
	afterPut error
	gone     bool
}

func (m *markersStub) Get(context.Context) (model.EncryptionMarker, bool, error) {
	switch {
	case m.getErr != nil:
		return model.EncryptionMarker{}, false, m.getErr
	case !m.written:
		return model.EncryptionMarker{}, false, nil
	case m.afterPut != nil:
		return model.EncryptionMarker{}, false, m.afterPut
	case m.gone:
		return model.EncryptionMarker{}, false, nil
	}
	return m.marker, true, nil
}

func (m *markersStub) Put(_ context.Context, marker model.EncryptionMarker) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.marker, m.written = marker, true
	return nil
}

// A host whose marker another host's, with another key, overwrote within the settle is refused.
func TestOpener_AdmitEncrypted_LosesTheRace(t *testing.T) {
	// Arrange
	ours, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	rival, err := ParseKey(newTestKey(t))
	require.NoError(t, err)
	store := &rivalMarkerStore{Storager: inmemory.New(""), rival: rival.Recipient().String()}

	// Act
	err = NewAdmitter().SetSettle(0).AdmitEncrypted(t.Context(), store, encryptionmarker.New(store), ours)

	// Assert
	require.ErrorIs(t, err, model.ErrWrongStoreKey)
	assert.ErrorContains(t, err, rival.Recipient().String())
}

// readMarkerObject is the marker's raw object, "" for none.
func readMarkerObject(t *testing.T, store storages.Storager) string {
	t.Helper()
	body, err := store.GetObject(t.Context(), encryptionmarker.FileName)
	if errors.Is(err, storages.ErrFileNotFound) {
		return ""
	}
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, body.Close()) })
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	return string(raw)
}

// rivalMarkerStore lands a rival host's marker right after every marker written.
type rivalMarkerStore struct {
	storages.Storager
	rival string
}

func (s *rivalMarkerStore) PutObject(ctx context.Context, key string, body io.Reader) error {
	if err := s.Storager.PutObject(ctx, key, body); err != nil {
		return err
	}
	return encryptionmarker.New(s.Storager).Put(ctx, model.NewEncryptionMarker(s.rival))
}
