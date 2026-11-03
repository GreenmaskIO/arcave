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
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/keyring"
)

const testPassword = "correct horse"

var testNow = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

func TestOpener_Open_CreatesAKeyOnFirstUse(t *testing.T) {
	// Arrange
	keys := newRealKeys()
	opener := newTestOpener(nil)

	// Act
	created, err := opener.Open(t.Context(), keys, holds(false), testPassword)
	require.NoError(t, err)
	reopened, err := newTestOpener(nil).Open(t.Context(), keys, holds(true), testPassword)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, created, reopened)
	assert.Equal(t, testNow, created.CreatedAt)
	assert.Equal(t, model.StoreKeyVersion, created.Version)
	assert.Len(t, created.ResticSecret, resticSecretSize)
	identity, err := ParseIdentity(created)
	require.NoError(t, err)
	assert.Equal(t, created.ID, model.NewStoreKeyID(identity.Recipient().String()))
}

func TestOpener_Open_refuses(t *testing.T) {
	tests := []struct {
		name      string
		stored    bool
		holdsData bool
		password  string
		wantErr   error
	}{
		{name: "an unencrypted project holding data", holdsData: true, password: testPassword, wantErr: model.ErrStoreUnencrypted},
		{name: "a password that opens no key", stored: true, holdsData: true, password: "wrong", wantErr: model.ErrWrongPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			keys := newRealKeys()
			if tt.stored {
				_, err := newTestOpener(nil).Open(t.Context(), keys, holds(false), testPassword)
				require.NoError(t, err)
			}

			// Act
			_, err := newTestOpener(nil).Open(t.Context(), keys, holds(tt.holdsData), tt.password)

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// A key another host wrote while this one settled wins whatever its id: this host deletes its
// own and opens the other's.
func TestOpener_Open_GivesWayToAnotherHostsKey(t *testing.T) {
	tests := []struct {
		name       string
		rivalLower bool
	}{
		{name: "the rival's id is lower", rivalLower: true},
		{name: "the rival's id is higher", rivalLower: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			keys := newRealKeys()
			var rival model.StoreKey
			opener := newTestOpener(func(ctx context.Context) {
				if rival.ID != "" {
					return
				}
				ids, err := keys.ListKeys(ctx)
				require.NoError(t, err)
				require.Len(t, ids, 1)
				rival = newRivalKey(t, func(id model.StoreKeyID) bool { return (id < ids[0]) == tt.rivalLower })
				require.NoError(t, keys.SaveKey(ctx, rival, testPassword))
			})

			// Act
			got, err := opener.Open(t.Context(), keys, holds(false), testPassword)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, rival, got)
			ids, err := keys.ListKeys(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []model.StoreKeyID{rival.ID}, ids, "this host's key is withdrawn")
		})
	}
}

// Open's calls in order: a refused or failed step goes no further, and a key is written only to
// an empty project, read back after the settle.
func TestOpener_Open(t *testing.T) {
	errStore := errors.New("store unreachable")
	stored := newRivalKey(t, func(model.StoreKeyID) bool { return true })
	lower := newRivalKey(t, func(id model.StoreKeyID) bool { return id < stored.ID })
	tests := []struct {
		name string
		// holdsData answers each call in turn, the last one repeated; none holds data when empty.
		holdsData []bool
		arrange   func(keys *mockKeys)
		wantKey   model.StoreKey
		// wantNew expects the key Open generated.
		wantNew bool
		wantErr error
	}{
		{
			name:      "an unencrypted project holding data writes no key",
			holdsData: []bool{true},
			arrange: func(keys *mockKeys) {
				keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Twice()
			},
			wantErr: model.ErrStoreUnencrypted,
		},
		{
			name:      "data beside a key another host just wrote is that host's",
			holdsData: []bool{true},
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Once(),
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{stored.ID}, nil).Twice(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
				)
			},
			wantKey: stored,
		},
		{
			name: "a key alone after the settle is created",
			arrange: func(keys *mockKeys) {
				var own model.StoreKeyID
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Once(),
					keys.On("SaveKey", mock.Anything, mock.MatchedBy(isNewKey), testPassword).
						Run(func(args mock.Arguments) { own = args.Get(1).(model.StoreKey).ID }).Return(nil).Once(),
					keys.On("ListKeys", mock.Anything).
						Return(func(context.Context) []model.StoreKeyID { return []model.StoreKeyID{own} }, nil).Once(),
				)
			},
			wantNew: true,
		},
		{
			name: "a key beside another host's is withdrawn, failing alone, and the other's loaded",
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Once(),
					keys.On("SaveKey", mock.Anything, mock.MatchedBy(isNewKey), testPassword).Return(nil).Once(),
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Once(),
					keys.On("DeleteKey", mock.Anything, mock.MatchedBy(isNewKeyID(lower.ID, stored.ID))).Return(errStore).Once(),
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(lower, nil).Once(),
				)
			},
			wantKey: lower,
		},
		{
			name:      "a key written while another host wrote plain data is withdrawn",
			holdsData: []bool{false, true},
			arrange: func(keys *mockKeys) {
				var own model.StoreKeyID
				ownKeys := func(context.Context) []model.StoreKeyID { return []model.StoreKeyID{own} }
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Once(),
					keys.On("SaveKey", mock.Anything, mock.MatchedBy(isNewKey), testPassword).
						Run(func(args mock.Arguments) { own = args.Get(1).(model.StoreKey).ID }).Return(nil).Once(),
					keys.On("ListKeys", mock.Anything).Return(ownKeys, nil).Twice(),
					keys.On("DeleteKey", mock.Anything, mock.MatchedBy(isNewKeyID())).Return(nil).Once(),
				)
			},
			wantErr: model.ErrStoreUnencrypted,
		},
		{
			name:      "a key that finds another host's key and data behind its settle gives way",
			holdsData: []bool{false, true},
			arrange: func(keys *mockKeys) {
				var own model.StoreKeyID
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Once(),
					keys.On("SaveKey", mock.Anything, mock.MatchedBy(isNewKey), testPassword).
						Run(func(args mock.Arguments) { own = args.Get(1).(model.StoreKey).ID }).Return(nil).Once(),
					keys.On("ListKeys", mock.Anything).
						Return(func(context.Context) []model.StoreKeyID { return []model.StoreKeyID{own} }, nil).Once(),
					keys.On("ListKeys", mock.Anything).
						Return(func(context.Context) []model.StoreKeyID { return []model.StoreKeyID{own, stored.ID} }, nil).Once(),
					keys.On("DeleteKey", mock.Anything, mock.MatchedBy(isNewKeyID(stored.ID))).Return(nil).Once(),
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
				)
			},
			wantKey: stored,
		},
		{
			name: "keys the password both opens are not chosen between until one goes",
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(lower, nil).Once(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
				)
			},
			wantKey: stored,
		},
		{
			name: "keys the password both opens for good are refused",
			arrange: func(keys *mockKeys) {
				keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Times(maxOpenAttempts)
				keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(lower, nil).Times(maxOpenAttempts)
				keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Times(maxOpenAttempts)
			},
			wantErr: model.ErrAmbiguousStoreKey,
		},
		{
			name: "a listing whose every key is gone is read again",
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(model.StoreKey{}, storages.ErrFileNotFound).Once(),
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
				)
			},
			wantKey: stored,
		},
		{
			name: "a key deleted since the listing is passed over",
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(model.StoreKey{}, storages.ErrFileNotFound).Once(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
				)
			},
			wantKey: stored,
		},
		{
			name: "a key the password does not open is passed over",
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(model.StoreKey{}, model.ErrWrongPassword).Once(),
					keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
				)
			},
			wantKey: stored,
		},
		{
			name: "a key that cannot be read fails the open",
			arrange: func(keys *mockKeys) {
				mock.InOrder(
					keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Once(),
					keys.On("LoadKey", mock.Anything, lower.ID, testPassword).Return(model.StoreKey{}, errStore).Once(),
				)
			},
			wantErr: errStore,
		},
		{
			name: "a listing that fails",
			arrange: func(keys *mockKeys) {
				keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID(nil), errStore).Once()
			},
			wantErr: errStore,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			keys := &mockKeys{}
			tt.arrange(keys)

			// Act
			got, err := newTestOpener(nil).Open(t.Context(), keys, holdsInTurn(tt.holdsData), testPassword)

			// Assert
			switch {
			case tt.wantErr != nil:
				assert.ErrorIs(t, err, tt.wantErr)
			case tt.wantNew:
				require.NoError(t, err)
				assert.True(t, isNewKey(got))
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.wantKey, got)
			}
			keys.AssertExpectations(t)
		})
	}
}

// A key opened once is not opened again: scrypt costs a second.
func TestOpener_Open_KeepsOpenedKeys(t *testing.T) {
	// Arrange
	stored := newRivalKey(t, func(model.StoreKeyID) bool { return true })
	keys := &mockKeys{}
	keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{stored.ID}, nil).Times(3)
	keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once()
	keys.On("LoadKey", mock.Anything, stored.ID, "wrong").Return(model.StoreKey{}, model.ErrWrongPassword).Once()
	opener := newTestOpener(nil)

	// Act
	first, firstErr := opener.Open(t.Context(), keys, holds(true), testPassword)
	second, secondErr := opener.Open(t.Context(), keys, holds(true), testPassword)
	_, wrongErr := opener.Open(t.Context(), keys, holds(true), "wrong")

	// Assert
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assert.Equal(t, stored, first)
	assert.Equal(t, stored, second)
	assert.ErrorIs(t, wrongErr, model.ErrWrongPassword)
	keys.AssertExpectations(t)
}

// A key this process opened holds beside a key another host is writing, which the same password
// opens: the other host withdraws it once its settle is over.
func TestOpener_Open_PrefersAKeyItOpened(t *testing.T) {
	// Arrange
	stored := newRivalKey(t, func(model.StoreKeyID) bool { return true })
	lower := newRivalKey(t, func(id model.StoreKeyID) bool { return id < stored.ID })
	keys := &mockKeys{}
	mock.InOrder(
		keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{stored.ID}, nil).Once(),
		keys.On("LoadKey", mock.Anything, stored.ID, testPassword).Return(stored, nil).Once(),
		keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{lower.ID, stored.ID}, nil).Once(),
	)
	opener := newTestOpener(nil)
	_, err := opener.Open(t.Context(), keys, holds(true), testPassword)
	require.NoError(t, err)

	// Act
	got, err := opener.Open(t.Context(), keys, holds(true), testPassword)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, stored, got)
	keys.AssertExpectations(t)
}

// A settle the command's cancellation cuts short fails the open with its cause.
func TestOpener_Open_CancelledSettle(t *testing.T) {
	// Arrange
	keys := &mockKeys{}
	mock.InOrder(
		keys.On("ListKeys", mock.Anything).Return([]model.StoreKeyID{}, nil).Once(),
		keys.On("SaveKey", mock.Anything, mock.MatchedBy(isNewKey), testPassword).Return(nil).Once(),
	)
	cause := errors.New("interrupted")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	opener := NewOpener().SetSettle(time.Hour)
	opener.now = func() time.Time { return testNow }

	// Act
	_, err := opener.Open(ctx, keys, holds(false), testPassword)

	// Assert
	assert.ErrorIs(t, err, cause)
	keys.AssertExpectations(t)
}

func TestRequireUnencrypted(t *testing.T) {
	tests := []struct {
		name    string
		ids     []model.StoreKeyID
		wantErr error
	}{
		{name: "no key", ids: []model.StoreKeyID{}},
		{name: "a key", ids: []model.StoreKeyID{"0123456789abcdef"}, wantErr: model.ErrStoreEncrypted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			keys := &mockKeys{}
			keys.On("ListKeys", mock.Anything).Return(tt.ids, nil).Once()

			// Act
			err := RequireUnencrypted(t.Context(), keys)

			// Assert
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			keys.AssertExpectations(t)
		})
	}
}

func TestNewResticPassword(t *testing.T) {
	// Arrange
	key := model.StoreKey{ResticSecret: bytes.Repeat([]byte{1}, resticSecretSize)}
	other := model.StoreKey{ResticSecret: bytes.Repeat([]byte{2}, resticSecretSize)}

	// Act
	password, err := NewResticPassword(key)
	require.NoError(t, err)
	again, err := NewResticPassword(key)
	require.NoError(t, err)
	otherPassword, err := NewResticPassword(other)
	require.NoError(t, err)

	// Assert
	assert.Len(t, password, 2*resticSecretSize)
	assert.Equal(t, password, again)
	assert.NotEqual(t, password, otherPassword)
}

func TestParseIdentity_RefusesAMismatchedID(t *testing.T) {
	// Arrange
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	key := model.StoreKey{ID: "0000000000000000", Identity: identity.String()}

	// Act
	_, err = ParseIdentity(key)

	// Assert
	assert.ErrorContains(t, err, "holds the identity of")
}

// newTestOpener settles at once, running onSettle in place of the wait.
func newTestOpener(onSettle func(ctx context.Context)) *Opener {
	opener := NewOpener()
	opener.now = func() time.Time { return testNow }
	opener.sleep = func(ctx context.Context, _ time.Duration) error {
		if onSettle != nil {
			onSettle(ctx)
		}
		return nil
	}
	return opener
}

func holds(data bool) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return data, nil }
}

// holdsInTurn answers each call with the next of answers, the last one repeated.
func holdsInTurn(answers []bool) func(context.Context) (bool, error) {
	calls := 0
	return func(context.Context) (bool, error) {
		if len(answers) == 0 {
			return false, nil
		}
		answer := answers[min(calls, len(answers)-1)]
		calls++
		return answer, nil
	}
}

// newRivalKey generates keys until one's id is accepted.
func newRivalKey(t *testing.T, accept func(model.StoreKeyID) bool) model.StoreKey {
	t.Helper()
	var identity *age.X25519Identity
	for identity == nil || !accept(model.NewStoreKeyID(identity.Recipient().String())) {
		var err error
		identity, err = age.GenerateX25519Identity()
		require.NoError(t, err)
	}
	return model.StoreKey{
		Version:      model.StoreKeyVersion,
		ID:           model.NewStoreKeyID(identity.Recipient().String()),
		CreatedAt:    testNow,
		Identity:     identity.String(),
		ResticSecret: bytes.Repeat([]byte{9}, resticSecretSize),
	}
}

// mockKeys is keyRepository.
type mockKeys struct{ mock.Mock }

func (m *mockKeys) ListKeys(ctx context.Context) ([]model.StoreKeyID, error) {
	args := m.Called(ctx)
	if ids, ok := args.Get(0).(func(context.Context) []model.StoreKeyID); ok {
		return ids(ctx), args.Error(1)
	}
	return args.Get(0).([]model.StoreKeyID), args.Error(1)
}

func (m *mockKeys) LoadKey(ctx context.Context, id model.StoreKeyID, password string) (model.StoreKey, error) {
	args := m.Called(ctx, id, password)
	return args.Get(0).(model.StoreKey), args.Error(1)
}

func (m *mockKeys) SaveKey(ctx context.Context, key model.StoreKey, password string) error {
	return m.Called(ctx, key, password).Error(0)
}

func (m *mockKeys) DeleteKey(ctx context.Context, id model.StoreKeyID) error {
	return m.Called(ctx, id).Error(0)
}

// isNewKey matches a key Open generated: its id is its identity's.
func isNewKey(key model.StoreKey) bool {
	identity, err := ParseIdentity(key)
	return err == nil && identity != nil && key.CreatedAt.Equal(testNow) && len(key.ResticSecret) == resticSecretSize
}

// isNewKeyID matches the id of a key Open generated: none of the fixtures' ids.
func isNewKeyID(fixtures ...model.StoreKeyID) func(model.StoreKeyID) bool {
	return func(id model.StoreKeyID) bool {
		return id != "0000000000000000" && !slices.Contains(fixtures, id)
	}
}

func newRealKeys() *keyring.Repository {
	return keyring.New(inmemory.New("")).SetWorkFactor(10)
}
