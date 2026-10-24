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

package sessionlock

import (
	"context"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/inmemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func newTestFischerStore(store storages.Storager, clock *fakeClock) *FischerSessionLockStore {
	return NewFischerSessionLockStore(store, testKey).SetClock(clock.now, clock.sleep)
}

func TestFischerSessionLockStore_Claim(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, store storages.Storager)
		// expected is the holder the claim saw: the zero record for none.
		expected model.SessionLockRecord
		// onSleep changes the store while the claim settles.
		onSleep    func(store storages.Storager) func(time.Duration)
		wantErr    error
		wantHolder model.OperationID
		wantSlept  time.Duration
	}{
		{
			name:       "a claim on no lock wins once it settles",
			plant:      func(*testing.T, storages.Storager) {},
			wantHolder: testOp,
			wantSlept:  DefaultSettle,
		},
		{
			name:       "a claim on a held lock is lost at once",
			plant:      func(t *testing.T, store storages.Storager) { plantLock(t, store, foreignLock) },
			wantErr:    ErrClaimLost,
			wantHolder: foreignOp,
		},
		{
			name:       "a claim from the holder it saw takes the lock over",
			plant:      func(t *testing.T, store storages.Storager) { plantLock(t, store, foreignLock) },
			expected:   foreignLock,
			wantHolder: testOp,
			wantSlept:  DefaultSettle,
		},
		{
			name:       "a claim from a holder that has since gone is lost",
			plant:      func(*testing.T, storages.Storager) {},
			expected:   foreignLock,
			wantErr:    ErrClaimLost,
			wantHolder: "",
		},
		{
			name: "a claim from a holder renewed since it was seen is lost",
			plant: func(t *testing.T, store storages.Storager) {
				renewed := foreignLock
				renewed.Revision = 1
				plantLock(t, store, renewed)
			},
			expected:   foreignLock,
			wantErr:    ErrClaimLost,
			wantHolder: foreignOp,
		},
		{
			name:       "a lock that does not decode is held by no one",
			plant:      func(t *testing.T, store storages.Storager) { plantRaw(t, store, "{torn") },
			wantHolder: testOp,
			wantSlept:  DefaultSettle,
		},
		{
			name:       "the claimer's own leftover claim is claimed again",
			plant:      func(t *testing.T, store storages.Storager) { plantLock(t, store, ownLock(0)) },
			expected:   ownLock(0),
			wantHolder: testOp,
			wantSlept:  DefaultSettle,
		},
		{
			name:  "a claim another overwrites while it settles is lost",
			plant: func(*testing.T, storages.Storager) {},
			onSleep: func(store storages.Storager) func(time.Duration) {
				return func(time.Duration) {
					_ = NewFischerSessionLockStore(store, testKey).write(context.Background(), foreignLock)
				}
			},
			wantErr:    ErrClaimLost,
			wantHolder: foreignOp,
			wantSlept:  DefaultSettle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("/")
			tt.plant(t, store)
			clock := &fakeClock{at: testStart}
			if tt.onSleep != nil {
				clock.onSleep = tt.onSleep(store)
			}
			fischer := newTestFischerStore(store, clock)

			// Act
			err := fischer.Claim(newTestContext(), ownLock(0), tt.expected)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantHolder, readStored(t, store).record.OperationID)
			assert.Equal(t, tt.wantSlept, clock.sleptFor())
		})
	}
}

// A claim a cancelled command interrupts while it settles is left in place: withdrawing it is
// the claimer's.
func TestFischerSessionLockStore_Claim_InterruptedIsLeftToItsClaimer(t *testing.T) {
	// Arrange
	clock := &fakeClock{at: testStart, sleepErr: context.Canceled}
	store := inmemory.New("/")
	fischer := newTestFischerStore(store, clock)

	// Act
	err := fischer.Claim(newTestContext(), ownLock(0), model.SessionLockRecord{})

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, testOp, readStored(t, store).record.OperationID)
}

func TestFischerSessionLockStore_Claim_FailedWrite(t *testing.T) {
	// Arrange
	clock := &fakeClock{at: testStart}
	store := &faultyStore{Storager: inmemory.New("/"), clock: clock, failWrites: true}
	fischer := newTestFischerStore(store, clock)

	// Act
	err := fischer.Claim(newTestContext(), ownLock(0), model.SessionLockRecord{})

	// Assert
	require.ErrorIs(t, err, errStore)
	assert.Zero(t, clock.sleptFor(), "a failed write does not settle")
}

func TestFischerSessionLockStore_Rewrite(t *testing.T) {
	tests := []struct {
		name         string
		plant        func(t *testing.T, store storages.Storager)
		wantErr      error
		wantHolder   model.OperationID
		wantRevision int64
	}{
		{
			name:         "the holder rewrites its lock",
			plant:        func(t *testing.T, store storages.Storager) { plantLock(t, store, ownLock(0)) },
			wantHolder:   testOp,
			wantRevision: 1,
		},
		{
			name:       "a lock another took over is left to it",
			plant:      func(t *testing.T, store storages.Storager) { plantLock(t, store, foreignLock) },
			wantErr:    ErrClaimLost,
			wantHolder: foreignOp,
		},
		{
			name:    "a deleted lock is not rewritten back",
			plant:   func(*testing.T, storages.Storager) {},
			wantErr: ErrClaimLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("/")
			tt.plant(t, store)
			fischer := newTestFischerStore(store, &fakeClock{at: testStart})

			// Act
			err := fischer.Rewrite(newTestContext(), ownLock(1))

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			stored := readStored(t, store)
			assert.Equal(t, tt.wantHolder, stored.record.OperationID)
			assert.Equal(t, tt.wantRevision, stored.record.Revision)
		})
	}
}

func TestFischerSessionLockStore_Remove(t *testing.T) {
	tests := []struct {
		name       string
		plant      func(t *testing.T, store storages.Storager)
		wantErr    error
		wantHolder model.OperationID
	}{
		{
			name:  "the holder removes its lock",
			plant: func(t *testing.T, store storages.Storager) { plantLock(t, store, ownLock(0)) },
		},
		{
			name:       "a lock another took over is left to it",
			plant:      func(t *testing.T, store storages.Storager) { plantLock(t, store, foreignLock) },
			wantErr:    ErrClaimLost,
			wantHolder: foreignOp,
		},
		{
			name:    "a lock already gone is not the holder's",
			plant:   func(*testing.T, storages.Storager) {},
			wantErr: ErrClaimLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("/")
			tt.plant(t, store)
			fischer := newTestFischerStore(store, &fakeClock{at: testStart})

			// Act
			err := fischer.Remove(newTestContext(), testOp)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			stored := readStored(t, store)
			assert.Equal(t, tt.wantHolder != "", stored.found)
			assert.Equal(t, tt.wantHolder, stored.record.OperationID)
		})
	}
}

func TestFischerSessionLockStore_Read(t *testing.T) {
	tests := []struct {
		name      string
		plant     func(t *testing.T, store storages.Storager)
		wantLock  model.SessionLockRecord
		wantFound bool
	}{
		{
			name:  "no lock",
			plant: func(*testing.T, storages.Storager) {},
		},
		{
			name:      "a held lock",
			plant:     func(t *testing.T, store storages.Storager) { plantLock(t, store, foreignLock) },
			wantLock:  foreignLock,
			wantFound: true,
		},
		{
			name:      "a lock that does not decode is found and held by no one",
			plant:     func(t *testing.T, store storages.Storager) { plantRaw(t, store, "{torn") },
			wantFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := inmemory.New("/")
			tt.plant(t, store)
			fischer := newTestFischerStore(store, &fakeClock{at: testStart})

			// Act
			lock, found, err := fischer.Read(newTestContext())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantLock, lock)
		})
	}
}

// A claim slower than the settle is not taken, and not deleted either: a late delete could take
// a claim another just settled. Its claimer finds its own operation there and claims again.
func TestFischerSessionLockStore_Claim_TooSlowIsLeftToItsClaimer(t *testing.T) {
	// Arrange
	clock := &fakeClock{at: testStart}
	store := &faultyStore{
		Storager: inmemory.New("/"),
		clock:    clock,
		delays:   []time.Duration{DefaultSettle + time.Second},
	}
	fischer := newTestFischerStore(store, clock)

	// Act
	err := fischer.Claim(newTestContext(), ownLock(0), model.SessionLockRecord{})

	// Assert
	require.ErrorIs(t, err, ErrClaimSlow)
	assert.Equal(t, testOp, readStored(t, store).record.OperationID)
	assert.Zero(t, clock.sleptFor(), "a slow claim does not settle")
}
