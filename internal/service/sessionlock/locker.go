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

// Package sessionlock holds a session's lock in the store, where every host sharing it sees it.
package sessionlock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

// Timing is how a Locker waits for and keeps a lock.
type Timing struct {
	// TTL is how long a lock nobody renews stays held, as each waiter measures it on its own clock.
	TTL time.Duration
	// Stale is how old its last renewal may be before the holder stops vouching for its lock. It
	// must exceed Renew and stay at most TTL less twice the I/O bound (6s): the gap to TTL is how
	// long work started after a fresh check is sure to run before another host may take over.
	Stale time.Duration
	// Renew is how often the holder renews.
	Renew time.Duration
	// Wait is how long Lock waits for a held lock to go before it refuses.
	Wait time.Duration
	// Poll is how often a waiter reads the lock.
	Poll time.Duration
}

// DefaultTiming is the timing every command runs with.
var DefaultTiming = Timing{
	// Half the TTL for Stale leaves 15s between the holder giving up and a takeover: room for the
	// store call a holder starts on a fresh check to land, and for a host that just woke to notice.
	TTL:   30 * time.Second,
	Stale: 15 * time.Second,
	Renew: 3 * time.Second,
	Wait:  60 * time.Second,
	Poll:  time.Second,
}

// Locker takes and keeps the lock of one session through a SessionLockStore: it waits for a held lock,
// takes over a stale one, renews its own and frees it.
type Locker struct {
	store   SessionLockStore
	session model.SessionID
	runner  heartbeat.RunnerInfo
	timing  Timing
	// now is the wall clock a holder ages its renewals by, so a host that slept counts the sleep;
	// elapsed is the clock a waiter measures by, which a clock step cannot move.
	now     func() time.Time
	elapsed func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
	// newOperationID names each lock's operation.
	newOperationID func() model.OperationID
}

// renewal is when a renewal began, on both clocks: a host that slept counts on the wall clock,
// a clock stepped back on the monotonic one, and the holder ages by whichever ran further.
type renewal struct {
	wall    time.Time
	elapsed time.Time
}

// observedSessionLock is what a waiter sees of a held lock: one whose view stands still is not renewed.
type observedSessionLock struct {
	holder   model.OperationID
	revision int64
}

// New returns a Locker of session's lock in store, naming runner as the holder.
func New(store SessionLockStore, session model.SessionID, runner heartbeat.RunnerInfo) *Locker {
	return &Locker{
		store:   store,
		session: session,
		runner:  runner,
		timing:  DefaultTiming,
		now:     wallClock,
		elapsed: time.Now,
		sleep:   sleepContext,

		newOperationID: model.NewOperationID,
	}
}

// SetTiming replaces DefaultTiming.
func (s *Locker) SetTiming(timing Timing) *Locker {
	s.timing = timing
	return s
}

// SetClock replaces both clocks with now, and the sleep a waiter pauses with.
func (s *Locker) SetClock(now func() time.Time, sleep func(ctx context.Context, d time.Duration) error) *Locker {
	s.now = now
	s.elapsed = now
	s.sleep = sleep
	return s
}

// Lock takes the session's lock for operation, waiting up to Timing.Wait, and returns ctx
// cancelled with model.ErrSessionLockLost once the lock is lost. The lock is renewed until Unlock,
// past a cancelled ctx too.
func (s *Locker) Lock(
	ctx context.Context, operation model.SessionLockOperation,
) (context.Context, model.GrantedSessionLock, error) {
	opID := s.newOperationID()
	lock, claimedAt, err := s.acquire(ctx, opID, operation)
	if err != nil {
		return nil, nil, err
	}
	lockCtx, cancel := context.WithCancelCause(ctx)
	grantedLock := &GrantedLock{
		locker:    s,
		opID:      opID,
		cancel:    cancel,
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
		lock:      lock,
		renewedAt: claimedAt,
	}
	go s.keep(context.WithoutCancel(ctx), grantedLock)
	zerolog.Ctx(ctx).Debug().Str("session", s.session.String()).Str("op_id", opID.String()).
		Str("operation", string(operation)).Msg("session lock taken")
	return lockCtx, grantedLock, nil
}

// verify refuses with model.ErrSessionLockLost when the store no longer holds grantedLock, or
// its last renewal is too old to vouch for it.
func (s *Locker) verify(ctx context.Context, grantedLock *GrantedLock) error {
	if grantedLock.unlocked.Load() {
		return s.newNotHeldError(grantedLock)
	}
	if err := s.requireFresh(grantedLock); err != nil {
		return err
	}
	current, found, err := s.store.Read(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", model.ErrSessionLockLost, err)
	}
	if !found || current.OperationID != grantedLock.opID {
		return fmt.Errorf("%w: session %s is now held by %s %q on %s", model.ErrSessionLockLost, s.session,
			current.Operation, current.OperationID, current.Host)
	}
	// The read may have taken long enough for the lock to age past vouching for the commit.
	return s.requireFresh(grantedLock)
}

// unlock removes grantedLock and stops renewing it; one already unlocked, too old to vouch for,
// or one the store no longer names its operation in is model.ErrSessionLockNotHeld.
func (s *Locker) unlock(ctx context.Context, grantedLock *GrantedLock) error {
	if !grantedLock.unlocked.CompareAndSwap(false, true) {
		return s.newNotHeldError(grantedLock)
	}
	close(grantedLock.quit)
	<-grantedLock.done
	grantedLock.cancel(nil)

	// A lock too old to vouch for may be another's by now: its file is not ours to remove.
	if err := s.requireFresh(grantedLock); err != nil {
		return fmt.Errorf("%w: %w", model.ErrSessionLockNotHeld, err)
	}
	// The lock goes even past a cancelled command: left in place it blocks the session a TTL.
	err := s.store.Remove(context.WithoutCancel(ctx), grantedLock.opID)
	if errors.Is(err, ErrClaimLost) {
		return fmt.Errorf("%w: session %s: %w", model.ErrSessionLockNotHeld, s.session, err)
	}
	if err != nil {
		return err
	}
	zerolog.Ctx(ctx).Debug().Str("session", s.session.String()).Str("op_id", grantedLock.opID.String()).
		Msg("session lock unlocked")
	return nil
}

// acquire claims the lock once it is free or stale; a claim this run made but could not settle
// is withdrawn on the way out, so it blocks no session for a TTL.
func (s *Locker) acquire(
	ctx context.Context, opID model.OperationID, operation model.SessionLockOperation,
) (model.SessionLockRecord, renewal, error) {
	lock, claimedAt, err := s.claimWhenFree(ctx, opID, operation)
	if err != nil {
		s.withdrawClaim(ctx, opID)
		return model.SessionLockRecord{}, renewal{}, err
	}
	return lock, claimedAt, nil
}

// claimWhenFree waits for the lock to be free or stale, then claims it from the holder it saw.
// A claim another won, or one too slow to settle, is waited out like any lock: the slow one may
// have landed after another claim settled.
func (s *Locker) claimWhenFree(
	ctx context.Context, opID model.OperationID, operation model.SessionLockOperation,
) (model.SessionLockRecord, renewal, error) {
	var (
		deadline  = s.elapsed().Add(s.timing.Wait)
		seen      *observedSessionLock
		seenSince time.Time
	)
	for {
		current, found, err := s.store.Read(ctx)
		if err != nil {
			return model.SessionLockRecord{}, renewal{}, err
		}
		// Counted from after the read: the write it returned is no older than that from here.
		seenAt := s.elapsed()
		if found {
			view := observedSessionLock{
				holder:   current.OperationID,
				revision: current.Revision,
			}
			if seen == nil || *seen != view {
				seen, seenSince = &view, seenAt
			}
			if seenAt.Sub(seenSince) < s.timing.TTL {
				if !seenAt.Before(deadline) {
					return model.SessionLockRecord{}, renewal{}, s.newLockedError(current, opID)
				}
				if err := s.sleep(ctx, s.timing.Poll); err != nil {
					return model.SessionLockRecord{}, renewal{}, err
				}
				continue
			}
			zerolog.Ctx(ctx).Warn().Str("session", s.session.String()).
				Str("holder", current.OperationID.String()).Str("host", current.Host).
				Msgf("the session lock was not renewed for %s; taking it over", s.timing.TTL)
		}

		claimedAt := renewal{
			wall:    s.now(),
			elapsed: s.elapsed(),
		}
		claim := model.SessionLockRecord{
			OperationID:   opID,
			Operation:     operation,
			Host:          s.runner.Host,
			PID:           s.runner.PID,
			ArcaveVersion: s.runner.ArcaveVersion,
			AcquiredAt:    claimedAt.wall,
		}
		err = s.store.Claim(ctx, claim, current)
		switch {
		case err == nil:
			return claim, claimedAt, nil
		case errors.Is(err, ErrClaimLost), errors.Is(err, ErrClaimSlow):
			zerolog.Ctx(ctx).Debug().Err(err).Msg("the session lock claim was not taken; waiting again")
			seen = nil
		default:
			return model.SessionLockRecord{}, renewal{}, err
		}
	}
}

// newLockedError refuses a lock still held past the wait; one held by this run's own claim is
// one that could not be settled in time.
func (s *Locker) newLockedError(current model.SessionLockRecord, opID model.OperationID) error {
	if current.OperationID == opID {
		return fmt.Errorf("%w: session %s: no claim settled within %s", model.ErrSessionLocked, s.session, s.timing.Wait)
	}
	return &model.SessionLockedError{
		Session: s.session,
		Holder:  current,
	}
}

// withdrawClaim removes opID's claim if the store still names it, past a cancelled ctx too; a
// failure is only logged, and the claim then goes stale.
func (s *Locker) withdrawClaim(ctx context.Context, opID model.OperationID) {
	err := s.store.Remove(context.WithoutCancel(ctx), opID)
	if err != nil && !errors.Is(err, ErrClaimLost) {
		zerolog.Ctx(ctx).Warn().Err(err).Str("session", s.session.String()).
			Msg("withdrawing the session lock claim; it goes stale instead")
	}
}

// keep renews grantedLock every Timing.Renew until it is unlocked, and cancels the operation once
// it cannot.
func (s *Locker) keep(ctx context.Context, grantedLock *GrantedLock) {
	defer close(grantedLock.done)
	ticker := time.NewTicker(s.timing.Renew)
	defer ticker.Stop()
	for {
		select {
		case <-grantedLock.quit:
			return
		case <-ticker.C:
			if err := s.renew(ctx, grantedLock); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).Str("session", s.session.String()).Msg("cancelling the operation")
				grantedLock.cancel(err)
				return
			}
		}
	}
}

// renew rewrites grantedLock with its next revision. A failed rewrite is retried on the next tick;
// a lock renewed too long ago, or one the store no longer names its operation in, is
// model.ErrSessionLockLost.
func (s *Locker) renew(ctx context.Context, grantedLock *GrantedLock) error {
	if err := s.requireFresh(grantedLock); err != nil {
		return err
	}
	startedAt := renewal{
		wall:    s.now(),
		elapsed: s.elapsed(),
	}
	grantedLock.mu.Lock()
	next := grantedLock.lock
	grantedLock.mu.Unlock()
	next.Revision++
	err := s.store.Rewrite(ctx, next)
	if errors.Is(err, ErrClaimLost) {
		return fmt.Errorf("%w: session %s: %w", model.ErrSessionLockLost, s.session, err)
	}
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("session lock renewal failed; retrying on the next tick")
		return nil
	}
	grantedLock.mu.Lock()
	defer grantedLock.mu.Unlock()
	grantedLock.lock, grantedLock.renewedAt = next, startedAt
	return nil
}

// requireFresh refuses a lock whose last renewal began Timing.Stale ago or earlier: a read and a
// write it starts now still land before a waiter may take it over.
func (s *Locker) requireFresh(grantedLock *GrantedLock) error {
	grantedLock.mu.Lock()
	defer grantedLock.mu.Unlock()
	age := max(s.now().Sub(grantedLock.renewedAt.wall), s.elapsed().Sub(grantedLock.renewedAt.elapsed))
	if age >= s.timing.Stale {
		return fmt.Errorf("%w: session %s not renewed for %s", model.ErrSessionLockLost, s.session,
			age.Round(time.Second))
	}
	return nil
}

// newNotHeldError refuses a lock already unlocked.
func (s *Locker) newNotHeldError(grantedLock *GrantedLock) error {
	return fmt.Errorf("%w: %s holds no lock of session %s", model.ErrSessionLockNotHeld, grantedLock.opID, s.session)
}

// wallClock reads the clock without its monotonic reading, which stops while the host sleeps.
func wallClock() time.Time {
	return time.Now().Round(0).UTC()
}
