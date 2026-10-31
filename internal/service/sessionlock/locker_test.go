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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// testTiming never renews on its own: a test renews by hand, or sets Renew to watch keep.
var testTiming = Timing{
	TTL:   DefaultTiming.TTL,
	Stale: DefaultTiming.Stale,
	Renew: time.Hour,
	Wait:  DefaultTiming.Wait,
	Poll:  DefaultTiming.Poll,
}

// holdFor is how long after its last renewal a holder still vouches for its lock.
var holdFor = DefaultTiming.Stale

// fakeSessionLockStore is a SessionLockStore that keeps the lock in memory and wins every claim its holder rule
// allows, unless told otherwise.
type fakeSessionLockStore struct {
	mu    sync.Mutex
	lock  model.SessionLockRecord
	found bool
	// claimErrs fail the next claims, in order; each but ErrClaimLost has landed all the same.
	claimErrs  []error
	rewriteErr error
	// claimedFrom is the holder each claim named.
	claimedFrom []model.OperationID
}

func newFakeSessionLockStore() *fakeSessionLockStore { return &fakeSessionLockStore{} }

func (f *fakeSessionLockStore) set(lock model.SessionLockRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lock, f.found = lock, true
}

func (f *fakeSessionLockStore) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lock, f.found = model.SessionLockRecord{}, false
}

func (f *fakeSessionLockStore) stored() (model.SessionLockRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lock, f.found
}

func (f *fakeSessionLockStore) Read(context.Context) (model.SessionLockRecord, bool, error) {
	lock, found := f.stored()
	return lock, found, nil
}

func (f *fakeSessionLockStore) Claim(_ context.Context, lock, expected model.SessionLockRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimedFrom = append(f.claimedFrom, expected.OperationID)
	if len(f.claimErrs) > 0 {
		err := f.claimErrs[0]
		f.claimErrs = f.claimErrs[1:]
		// A slow or interrupted claim may have landed, as a real one may: only it is not known to have won.
		if !errors.Is(err, ErrClaimLost) {
			f.lock, f.found = lock, true
		}
		return err
	}
	if f.lock.OperationID != expected.OperationID || f.lock.Revision != expected.Revision {
		return ErrClaimLost
	}
	f.lock, f.found = lock, true
	return nil
}

func (f *fakeSessionLockStore) Rewrite(_ context.Context, lock model.SessionLockRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rewriteErr != nil {
		return f.rewriteErr
	}
	if !f.found || f.lock.OperationID != lock.OperationID {
		return ErrClaimLost
	}
	f.lock = lock
	return nil
}

func (f *fakeSessionLockStore) Remove(ctx context.Context, opID model.OperationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.found || f.lock.OperationID != opID {
		return ErrClaimLost
	}
	f.lock, f.found = model.SessionLockRecord{}, false
	return nil
}

// newTestLocker is a Locker whose every lock is testOp's.
func newTestLocker(store SessionLockStore, clock *fakeClock) *Locker {
	locker := New(store, testSession, testRunner).SetClock(clock.now, clock.sleep).SetTiming(testTiming)
	locker.newOperationID = func() model.OperationID { return testOp }
	return locker
}

// lockForTest takes testOp's lock and unlocks it when the test ends, whatever it left.
func lockForTest(t *testing.T, locker *Locker) (context.Context, *GrantedLock) {
	t.Helper()
	lockCtx, grantedLock, err := locker.Lock(newTestContext(), model.SessionLockOperationSavepointCreate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grantedLock.Unlock(newTestContext()) })
	return lockCtx, grantedLock.(*GrantedLock)
}

func TestLocker_Lock(t *testing.T) {
	tests := []struct {
		name string
		// arrange prepares the lock store; onSleep changes it while the locker waits.
		arrange func(store *fakeSessionLockStore)
		onSleep func(store *fakeSessionLockStore, sleeps *int) func(d time.Duration)
		// wantSlept is what the lock cost in pauses, on the fake clock; wantFrom the holders claimed from.
		wantSlept time.Duration
		wantFrom  []model.OperationID
	}{
		{
			name:     "a free session is claimed at once",
			arrange:  func(*fakeSessionLockStore) {},
			wantFrom: []model.OperationID{""},
		},
		{
			name:    "a held lock is waited for until it is freed",
			arrange: func(store *fakeSessionLockStore) { store.set(foreignLock) },
			onSleep: func(store *fakeSessionLockStore, sleeps *int) func(time.Duration) {
				return func(time.Duration) {
					if *sleeps++; *sleeps == 3 {
						store.clear()
					}
				}
			},
			wantSlept: 3 * testTiming.Poll,
			wantFrom:  []model.OperationID{""},
		},
		{
			name:      "a lock nobody renews is taken over from its holder after the TTL",
			arrange:   func(store *fakeSessionLockStore) { store.set(foreignLock) },
			wantSlept: testTiming.TTL,
			wantFrom:  []model.OperationID{foreignOp},
		},
		{
			name:      "a lock that does not decode goes stale like any other",
			arrange:   func(store *fakeSessionLockStore) { store.set(model.SessionLockRecord{}) },
			wantSlept: testTiming.TTL,
			wantFrom:  []model.OperationID{""},
		},
		{
			name:      "the operation's own leftover claim is waited out like any lock",
			arrange:   func(store *fakeSessionLockStore) { store.set(ownLock(0)) },
			wantSlept: testTiming.TTL,
			wantFrom:  []model.OperationID{testOp},
		},
		{
			name:     "a claim another won is followed by a fresh look",
			arrange:  func(store *fakeSessionLockStore) { store.claimErrs = []error{ErrClaimLost} },
			wantFrom: []model.OperationID{"", ""},
		},
		{
			name:      "a claim too slow to be safe is waited out, then made again",
			arrange:   func(store *fakeSessionLockStore) { store.claimErrs = []error{ErrClaimSlow} },
			wantSlept: testTiming.TTL,
			wantFrom:  []model.OperationID{"", testOp},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newFakeSessionLockStore()
			tt.arrange(store)
			clock := &fakeClock{at: testStart}
			var sleeps int
			if tt.onSleep != nil {
				clock.onSleep = tt.onSleep(store, &sleeps)
			}
			locker := newTestLocker(store, clock)

			// Act
			lockCtx, _ := lockForTest(t, locker)

			// Assert
			require.NoError(t, lockCtx.Err())
			stored, found := store.stored()
			require.True(t, found)
			assert.Equal(t, model.SessionLockRecord{
				OperationID:   testOp,
				Operation:     model.SessionLockOperationSavepointCreate,
				Host:          testRunner.Host,
				PID:           testRunner.PID,
				ArcaveVersion: testRunner.ArcaveVersion,
				AcquiredAt:    testStart.Add(tt.wantSlept),
			}, stored)
			assert.Equal(t, tt.wantSlept, clock.sleptFor())
			assert.Equal(t, tt.wantFrom, store.claimedFrom)
		})
	}
}

// warningRecorder keeps the messages of every warned event.
type warningRecorder struct {
	warnings []string
}

func (r *warningRecorder) Render(evt progress.Event) {
	if evt.Status == progress.StatusWarned {
		r.warnings = append(r.warnings, evt.Msg)
	}
}

// The wait warns once per holder it sees: a holder's renewals are not news.
func TestLocker_Lock_WarnsOncePerHolder(t *testing.T) {
	// Arrange
	store := newFakeSessionLockStore()
	store.set(foreignLock)
	clock := &fakeClock{at: testStart}
	var sleeps int
	clock.onSleep = func(time.Duration) {
		sleeps++
		switch sleeps {
		case 1:
			renewed := foreignLock
			renewed.Revision++
			store.set(renewed)
		case 2:
			next := foreignLock
			next.OperationID, next.Operation = "019e843e-1650-7000-8000-000000000003", model.SessionLockOperationRollback
			store.set(next)
		case 3:
			store.clear()
		}
	}
	locker := newTestLocker(store, clock)
	recorder := &warningRecorder{}
	ctx := progress.WithTracker(newTestContext(), progress.New(recorder, clock.now))
	ctx, step := progress.Begin(ctx, progress.Step{Kind: "session.lock"})

	// Act
	_, grantedLock, err := locker.Lock(ctx, model.SessionLockOperationSavepointCreate)
	step.End(err)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { _ = grantedLock.Unlock(newTestContext()) })
	assert.Equal(t, []string{
		"waiting: held by gc on other (pid 7)",
		"waiting: held by rollback on other (pid 7)",
	}, recorder.warnings)
}

// A claim lost to another is followed by a fresh look, which warns of no holder already warned of.
func TestLocker_Lock_WarnsOncePerHolderPastALostClaim(t *testing.T) {
	// Arrange
	store := newFakeSessionLockStore()
	store.set(foreignLock)
	store.claimErrs = []error{ErrClaimLost}
	clock := &fakeClock{at: testStart}
	locker := newTestLocker(store, clock)
	recorder := &warningRecorder{}
	ctx := progress.WithTracker(newTestContext(), progress.New(recorder, clock.now))
	ctx, step := progress.Begin(ctx, progress.Step{Kind: "session.lock"})

	// Act
	_, grantedLock, err := locker.Lock(ctx, model.SessionLockOperationSavepointCreate)
	step.End(err)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { _ = grantedLock.Unlock(newTestContext()) })
	assert.Equal(t, []model.OperationID{foreignOp, foreignOp}, store.claimedFrom)
	assert.Equal(t, []string{"waiting: held by gc on other (pid 7)"}, recorder.warnings)
}

func TestLocker_Lock_Refused(t *testing.T) {
	tests := []struct {
		name string
		// arrange prepares the lock store; onSleep changes it while the locker waits.
		arrange    func(store *fakeSessionLockStore)
		onSleep    func(store *fakeSessionLockStore) func(d time.Duration)
		wantErr    error
		wantHolder model.OperationID
		// wantStoreEmpty is whether the run withdrew a claim of its own on the way out.
		wantStoreEmpty bool
	}{
		{
			name:    "a lock its holder keeps renewing is refused after the wait",
			arrange: func(store *fakeSessionLockStore) { store.set(foreignLock) },
			onSleep: func(store *fakeSessionLockStore) func(time.Duration) {
				renewed := foreignLock
				return func(time.Duration) {
					renewed.Revision++
					store.set(renewed)
				}
			},
			wantErr:    model.ErrSessionLocked,
			wantHolder: foreignOp,
		},
		{
			name: "claims too slow until the wait runs out are refused",
			arrange: func(store *fakeSessionLockStore) {
				for range int(testTiming.Wait/testTiming.Poll) + 1 {
					store.claimErrs = append(store.claimErrs, ErrClaimSlow)
				}
			},
			wantErr:        model.ErrSessionLocked,
			wantStoreEmpty: true,
		},
		{
			name:           "a claim the store fails is withdrawn and fails the lock",
			arrange:        func(store *fakeSessionLockStore) { store.claimErrs = []error{errStore} },
			wantErr:        errStore,
			wantStoreEmpty: true,
		},
		{
			name:           "a claim a cancelled command interrupts is withdrawn",
			arrange:        func(store *fakeSessionLockStore) { store.claimErrs = []error{context.Canceled} },
			wantErr:        context.Canceled,
			wantStoreEmpty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newFakeSessionLockStore()
			tt.arrange(store)
			clock := &fakeClock{at: testStart}
			if tt.onSleep != nil {
				clock.onSleep = tt.onSleep(store)
			}
			locker := newTestLocker(store, clock)

			// Act
			_, _, err := locker.Lock(newTestContext(), model.SessionLockOperationSavepointCreate)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			if tt.wantStoreEmpty {
				_, found := store.stored()
				assert.False(t, found, "a claim the run could not settle is withdrawn")
			}
			if tt.wantHolder != "" {
				var locked *model.SessionLockedError
				require.ErrorAs(t, err, &locked)
				assert.Equal(t, testSession, locked.Session)
				assert.Equal(t, tt.wantHolder, locked.Holder.OperationID)
				assert.Equal(t, testTiming.Wait, clock.sleptFor())
			}
		})
	}
}

// The context Lock returns ends once keep finds the lock can no longer be renewed.
func TestLocker_Lock_LostLockCancelsTheOperation(t *testing.T) {
	tests := []struct {
		name   string
		change func(store *fakeSessionLockStore, clock *fakeClock)
	}{
		{
			name:   "a lock left unrenewed too long",
			change: func(_ *fakeSessionLockStore, clock *fakeClock) { clock.advance(holdFor) },
		},
		{
			name:   "a lock another operation took over",
			change: func(store *fakeSessionLockStore, _ *fakeClock) { store.set(foreignLock) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newFakeSessionLockStore()
			clock := &fakeClock{at: testStart}
			timing := testTiming
			timing.Renew = time.Millisecond
			locker := newTestLocker(store, clock).SetTiming(timing)
			lockCtx, _ := lockForTest(t, locker)

			// Act
			tt.change(store, clock)

			// Assert
			select {
			case <-lockCtx.Done():
			case <-time.After(5 * time.Second):
				require.FailNow(t, "the lost lock did not cancel the operation")
			}
			require.ErrorIs(t, context.Cause(lockCtx), model.ErrSessionLockLost)
		})
	}
}

func TestLocker_Verify(t *testing.T) {
	tests := []struct {
		name string
		// change runs on a locked session before Verify.
		change  func(t *testing.T, store *fakeSessionLockStore, clock *fakeClock, grantedLock *GrantedLock)
		wantErr error
	}{
		{
			name:   "the holder's own lock verifies",
			change: func(*testing.T, *fakeSessionLockStore, *fakeClock, *GrantedLock) {},
		},
		{
			name: "a lock another operation took over is lost",
			change: func(_ *testing.T, store *fakeSessionLockStore, _ *fakeClock, _ *GrantedLock) {
				store.set(foreignLock)
			},
			wantErr: model.ErrSessionLockLost,
		},
		{
			name:    "a deleted lock is lost",
			change:  func(_ *testing.T, store *fakeSessionLockStore, _ *fakeClock, _ *GrantedLock) { store.clear() },
			wantErr: model.ErrSessionLockLost,
		},
		{
			name: "a lock left unrenewed too long is lost",
			change: func(_ *testing.T, _ *fakeSessionLockStore, clock *fakeClock, _ *GrantedLock) {
				clock.advance(holdFor)
			},
			wantErr: model.ErrSessionLockLost,
		},
		{
			name: "an unlocked lock is not held",
			change: func(t *testing.T, _ *fakeSessionLockStore, _ *fakeClock, grantedLock *GrantedLock) {
				require.NoError(t, grantedLock.Unlock(newTestContext()))
			},
			wantErr: model.ErrSessionLockNotHeld,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newFakeSessionLockStore()
			clock := &fakeClock{at: testStart}
			locker := newTestLocker(store, clock)
			_, grantedLock := lockForTest(t, locker)
			tt.change(t, store, clock, grantedLock)

			// Act
			err := grantedLock.Verify(newTestContext())

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A wall clock stepped back does not keep a holder vouching for a lock the monotonic clock
// says it stopped renewing; a slept host, whose monotonic clock stood still, ages on the wall.
func TestLocker_Verify_AgesByTheClockThatRanFurther(t *testing.T) {
	tests := []struct {
		name                 string
		wallStep, elapsedRun time.Duration
	}{
		{name: "the wall clock stepped back", wallStep: -time.Hour, elapsedRun: holdFor},
		{name: "the host slept", wallStep: holdFor, elapsedRun: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			wall := &fakeClock{at: testStart}
			elapsed := &fakeClock{at: testStart}
			locker := newTestLocker(newFakeSessionLockStore(), wall)
			locker.elapsed = elapsed.now
			_, grantedLock := lockForTest(t, locker)
			wall.advance(tt.wallStep)
			elapsed.advance(tt.elapsedRun)

			// Act
			err := grantedLock.Verify(newTestContext())

			// Assert
			require.ErrorIs(t, err, model.ErrSessionLockLost)
		})
	}
}

func TestLocker_unlock(t *testing.T) {
	cancelled, cancel := context.WithCancel(newTestContext())
	cancel()
	tests := []struct {
		name string
		// change runs on a locked session before the unlock.
		change     func(t *testing.T, store *fakeSessionLockStore, clock *fakeClock, grantedLock *GrantedLock)
		ctx        context.Context
		wantErr    error
		wantHolder model.OperationID
	}{
		{
			name:   "the holder unlocks its lock",
			change: func(*testing.T, *fakeSessionLockStore, *fakeClock, *GrantedLock) {},
			ctx:    newTestContext(),
		},
		{
			name:   "a cancelled command still unlocks its lock",
			change: func(*testing.T, *fakeSessionLockStore, *fakeClock, *GrantedLock) {},
			ctx:    cancelled,
		},
		{
			name: "a lock another operation took over is left to it",
			change: func(_ *testing.T, store *fakeSessionLockStore, _ *fakeClock, _ *GrantedLock) {
				store.set(foreignLock)
			},
			ctx:        newTestContext(),
			wantErr:    model.ErrSessionLockNotHeld,
			wantHolder: foreignOp,
		},
		{
			name:    "a lock already gone is not held",
			change:  func(_ *testing.T, store *fakeSessionLockStore, _ *fakeClock, _ *GrantedLock) { store.clear() },
			ctx:     newTestContext(),
			wantErr: model.ErrSessionLockNotHeld,
		},
		{
			name: "a lock too old to vouch for leaves its file",
			change: func(_ *testing.T, _ *fakeSessionLockStore, clock *fakeClock, _ *GrantedLock) {
				clock.advance(holdFor)
			},
			ctx:        newTestContext(),
			wantErr:    model.ErrSessionLockNotHeld,
			wantHolder: testOp,
		},
		{
			name: "a lock unlocked twice is unlocked once",
			change: func(t *testing.T, _ *fakeSessionLockStore, _ *fakeClock, grantedLock *GrantedLock) {
				require.NoError(t, grantedLock.Unlock(newTestContext()))
			},
			ctx:     newTestContext(),
			wantErr: model.ErrSessionLockNotHeld,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newFakeSessionLockStore()
			clock := &fakeClock{at: testStart}
			locker := newTestLocker(store, clock)
			lockCtx, grantedLock := lockForTest(t, locker)
			tt.change(t, store, clock, grantedLock)

			// Act
			err := grantedLock.Unlock(tt.ctx)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			stored, found := store.stored()
			assert.Equal(t, tt.wantHolder != "", found)
			assert.Equal(t, tt.wantHolder, stored.OperationID)
			assert.Error(t, lockCtx.Err(), "an unlocked lock ends its context")
		})
	}
}

func TestLocker_renew(t *testing.T) {
	tests := []struct {
		name string
		// change runs on a locked session before renew; after runs once it renewed.
		change       func(store *fakeSessionLockStore, clock *fakeClock)
		after        func(clock *fakeClock)
		wantErr      error
		wantRevision int64
		// wantFresh is whether Verify still vouches for the lock after.
		wantFresh bool
	}{
		{
			name:         "a renewal writes the next revision and keeps the lock fresh",
			change:       func(_ *fakeSessionLockStore, clock *fakeClock) { clock.advance(holdFor / 2) },
			after:        func(clock *fakeClock) { clock.advance(holdFor/2 + time.Second) },
			wantRevision: 1,
			wantFresh:    true,
		},
		{
			name:         "a failed rewrite is retried on the next tick",
			change:       func(store *fakeSessionLockStore, _ *fakeClock) { store.rewriteErr = errStore },
			wantRevision: 0,
		},
		{
			name:    "a lock another operation took over is lost",
			change:  func(store *fakeSessionLockStore, _ *fakeClock) { store.set(foreignLock) },
			wantErr: model.ErrSessionLockLost,
		},
		{
			name:    "a deleted lock is lost",
			change:  func(store *fakeSessionLockStore, _ *fakeClock) { store.clear() },
			wantErr: model.ErrSessionLockLost,
		},
		{
			name:    "a lock last renewed too long ago is lost",
			change:  func(_ *fakeSessionLockStore, clock *fakeClock) { clock.advance(holdFor) },
			wantErr: model.ErrSessionLockLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newFakeSessionLockStore()
			clock := &fakeClock{at: testStart}
			locker := newTestLocker(store, clock)
			_, grantedLock := lockForTest(t, locker)
			tt.change(store, clock)

			// Act
			err := locker.renew(newTestContext(), grantedLock)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			stored, _ := store.stored()
			assert.Equal(t, testOp, stored.OperationID)
			assert.Equal(t, tt.wantRevision, stored.Revision)
			if tt.after != nil {
				tt.after(clock)
				assert.Equal(t, tt.wantFresh, grantedLock.Verify(newTestContext()) == nil)
			}
		})
	}
}

// Two operations on one session, as two hosts sharing a store run them, through the lock and
// the store a command builds: the second waits for the first to unlock, then holds the lock.
func TestLocker_Lock_SecondOperationWaitsForTheFirst(t *testing.T) {
	// Arrange
	ctx := newTestContext()
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	clock := &fakeClock{at: testStart}
	newLocker := func() *Locker {
		return newTestLocker(newTestFischerStore(store, clock), clock)
	}
	first := newLocker()
	first.newOperationID = func() model.OperationID { return foreignOp }
	_, firstLock, err := first.Lock(ctx, model.SessionLockOperationGarbageCollect)
	require.NoError(t, err)
	clock.slept = 0
	second := newLocker()
	var sleeps int
	clock.onSleep = func(time.Duration) {
		if sleeps++; sleeps == 2 {
			assert.NoError(t, firstLock.Unlock(ctx))
		}
	}

	// Act
	lockForTest(t, second)

	// Assert
	assert.Equal(t, 2*testTiming.Poll+DefaultSettle, clock.sleptFor())
	assert.Equal(t, testOp, readStored(t, store).record.OperationID)
	require.ErrorIs(t, firstLock.Verify(ctx), model.ErrSessionLockNotHeld)
}

// A lock too old to vouch for may already be another's: unlocking it leaves its file alone.
func TestLocker_unlock_StaleLockKeepsItsFile(t *testing.T) {
	// Arrange
	store := newFakeSessionLockStore()
	clock := &fakeClock{at: testStart}
	locker := newTestLocker(store, clock)
	_, grantedLock, err := locker.Lock(newTestContext(), model.SessionLockOperationSavepointCreate)
	require.NoError(t, err)
	clock.advance(holdFor)

	// Act
	err = grantedLock.Unlock(newTestContext())

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	stored, found := store.stored()
	require.True(t, found)
	assert.Equal(t, testOp, stored.OperationID)
}
