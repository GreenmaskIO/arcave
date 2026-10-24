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
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

const (
	testKey   = "019e843e-1650-7000-8000-0000000000aa.lock.json"
	testOp    = model.OperationID("019e843e-1650-7000-8000-000000000001")
	foreignOp = model.OperationID("019e843e-1650-7000-8000-000000000002")
)

var (
	testSession = model.DefaultSession
	testRunner  = heartbeat.RunnerInfo{Host: "box", PID: 42, ArcaveVersion: "dev"}
	testStart   = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	errStore    = errors.New("store hiccup")
	foreignLock = model.SessionLockRecord{
		OperationID: foreignOp,
		Operation:   model.SessionLockOperationGarbageCollect,
		Host:        "other",
		PID:         7,
		AcquiredAt:  testStart.Add(-time.Minute),
	}
)

// fakeClock is a clock a sleep advances; onSleep sees each pause before the clock moves.
type fakeClock struct {
	mu      sync.Mutex
	at      time.Time
	slept   time.Duration
	onSleep func(d time.Duration)
	// sleepErr ends every pause, as a cancelled command does.
	sleepErr error
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	if c.onSleep != nil {
		c.onSleep(d)
	}
	if c.sleepErr != nil {
		return c.sleepErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
	c.slept += d
	return nil
}

func (c *fakeClock) sleptFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept
}

// faultyStore is a store whose writes take the clock forward by the next of delays, and whose
// writes fail once told to.
type faultyStore struct {
	storages.Storager
	clock      *fakeClock
	delays     []time.Duration
	failWrites bool
}

func (s *faultyStore) PutObject(ctx context.Context, key string, body io.Reader) error {
	if s.failWrites {
		return errStore
	}
	if len(s.delays) > 0 {
		s.clock.advance(s.delays[0])
		s.delays = s.delays[1:]
	}
	return s.Storager.PutObject(ctx, key, body)
}

func newTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
}

func plantLock(t *testing.T, store storages.Storager, lock model.SessionLockRecord) {
	t.Helper()
	body, err := json.Marshal(lock)
	require.NoError(t, err)
	plantRaw(t, store, string(body))
}

func plantRaw(t *testing.T, store storages.Storager, body string) {
	t.Helper()
	require.NoError(t, store.PutObject(context.Background(), testKey, strings.NewReader(body)))
}

// storedLock is store's lock file as a FischerSessionLockStore reads it.
type storedLock struct {
	record model.SessionLockRecord
	found  bool
}

func readStored(t *testing.T, store storages.Storager) storedLock {
	t.Helper()
	record, found, err := NewFischerSessionLockStore(store, testKey).Read(newTestContext())
	require.NoError(t, err)
	return storedLock{
		record: record,
		found:  found,
	}
}

// ownLock is testOp's lock as a test plants or expects it.
func ownLock(revision int64) model.SessionLockRecord {
	return model.SessionLockRecord{
		OperationID: testOp,
		Operation:   model.SessionLockOperationSavepointCreate,
		Host:        testRunner.Host,
		PID:         testRunner.PID,
		AcquiredAt:  testStart,
		Revision:    revision,
	}
}
