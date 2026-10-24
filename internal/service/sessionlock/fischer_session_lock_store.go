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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// DefaultSettle is how long a claim waits before it reads itself back.
const DefaultSettle = time.Second

// ioTimeout bounds one read or write of the lock.
const ioTimeout = 3 * time.Second

// FischerSessionLockStore keeps a session's lock file in a store that can only read and overwrite,
// and makes a contended claim Fischer's: write, settle, read back — the last writer wins, while
// every claim's read and write fit in the settle.
type FischerSessionLockStore struct {
	store  storages.Storager
	key    string
	settle time.Duration
	// elapsed measures a claim's duration on a clock a step cannot move.
	elapsed func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
}

// NewFischerSessionLockStore keeps the lock file at key of store, as storefactory.SessionLock names them.
func NewFischerSessionLockStore(store storages.Storager, key string) *FischerSessionLockStore {
	return &FischerSessionLockStore{
		store:   store,
		key:     key,
		settle:  DefaultSettle,
		elapsed: time.Now,
		sleep:   sleepContext,
	}
}

// SetSettle replaces DefaultSettle.
func (s *FischerSessionLockStore) SetSettle(settle time.Duration) *FischerSessionLockStore {
	s.settle = settle
	return s
}

// SetClock replaces the clock a claim is timed on and the sleep it settles with.
func (s *FischerSessionLockStore) SetClock(
	elapsed func() time.Time, sleep func(ctx context.Context, d time.Duration) error,
) *FischerSessionLockStore {
	s.elapsed = elapsed
	s.sleep = sleep
	return s
}

// Read returns the lock file's record; one that does not decode is found with a zero record.
func (s *FischerSessionLockStore) Read(ctx context.Context) (model.SessionLockRecord, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	file, err := s.store.GetObject(ctx, s.key)
	if errors.Is(err, storages.ErrFileNotFound) {
		return model.SessionLockRecord{}, false, nil
	}
	if err != nil {
		return model.SessionLockRecord{}, false, fmt.Errorf("read session lock: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("closing the session lock")
		}
	}()
	body, err := io.ReadAll(file)
	if err != nil {
		return model.SessionLockRecord{}, false, fmt.Errorf("read session lock: %w", err)
	}
	var record model.SessionLockRecord
	if err := json.Unmarshal(body, &record); err != nil {
		// A torn write: it goes stale like any lock nobody renews.
		zerolog.Ctx(ctx).Warn().Err(err).Msg("the session lock does not decode")
		return model.SessionLockRecord{}, true, nil
	}
	return record, true, nil
}

// Claim writes lock while the store still holds expected, settles and reads it back. A claim
// another overwrote is ErrClaimLost; one slower than the settle is ErrClaimSlow. A claim that does
// not win is left in place, landed or not: withdrawing it is the claimer's.
func (s *FischerSessionLockStore) Claim(
	ctx context.Context, lock model.SessionLockRecord, expected model.SessionLockRecord,
) error {
	readAt := s.elapsed()
	current, _, err := s.Read(ctx)
	if err != nil {
		return err
	}
	if !sameHolding(current, expected) {
		return fmt.Errorf("%w: held by %q at revision %d, not %q at %d", ErrClaimLost,
			current.OperationID, current.Revision, expected.OperationID, expected.Revision)
	}
	if err := s.write(ctx, lock); err != nil {
		return err
	}
	if took := s.elapsed().Sub(readAt); took > s.settle {
		return fmt.Errorf("%w: took %s, longer than %s", ErrClaimSlow, took, s.settle)
	}
	if err := s.sleep(ctx, s.settle); err != nil {
		return err
	}
	settled, _, err := s.Read(ctx)
	if err != nil {
		return err
	}
	if settled.OperationID != lock.OperationID {
		return fmt.Errorf("%w: claimed by %q meanwhile", ErrClaimLost, settled.OperationID)
	}
	return nil
}

// Rewrite replaces the lock while the store names lock.OperationID. Only a takeover past the
// TTL competes with its holder, so it does not settle.
func (s *FischerSessionLockStore) Rewrite(ctx context.Context, lock model.SessionLockRecord) error {
	if err := s.requireHolder(ctx, lock.OperationID); err != nil {
		return err
	}
	return s.write(ctx, lock)
}

// Remove deletes the lock file while the store names opID; one already gone is ErrClaimLost.
func (s *FischerSessionLockStore) Remove(ctx context.Context, opID model.OperationID) error {
	if err := s.requireHolder(ctx, opID); err != nil {
		return err
	}
	return s.delete(ctx)
}

// sameHolding reports whether two records name the same holder at the same revision; a missing
// lock and one that does not decode are both the zero record.
func sameHolding(current, expected model.SessionLockRecord) bool {
	return current.OperationID == expected.OperationID && current.Revision == expected.Revision
}

// requireHolder refuses unless the store names opID as the holder.
func (s *FischerSessionLockStore) requireHolder(ctx context.Context, opID model.OperationID) error {
	current, found, err := s.Read(ctx)
	if err != nil {
		return err
	}
	if !found || current.OperationID != opID {
		return fmt.Errorf("%w: held by %q, not %q", ErrClaimLost, current.OperationID, opID)
	}
	return nil
}

// write stores lock in the lock file, replacing whatever is there.
func (s *FischerSessionLockStore) write(ctx context.Context, lock model.SessionLockRecord) error {
	data, err := json.Marshal(lock)
	if err != nil {
		return fmt.Errorf("encode session lock: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	if err := s.store.PutObject(ctx, s.key, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write session lock: %w", err)
	}
	return nil
}

// delete drops the lock file; one already gone is not an error.
func (s *FischerSessionLockStore) delete(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	if err := s.store.Delete(ctx, s.key); err != nil && !errors.Is(err, storages.ErrFileNotFound) {
		return fmt.Errorf("delete session lock: %w", err)
	}
	return nil
}

// sleepContext pauses for d, or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}
