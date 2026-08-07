// Copyright 2025 Greenmask
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

package heartbeat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// testInterval is short enough that the ticker fires while a test waits on it,
// and beatTimeout is the bound that turns a worker that never writes into a
// failure rather than a hang.
const (
	testInterval = 10 * time.Millisecond
	beatTimeout  = 5 * time.Second
)

// waitBeat blocks until the writer records one more call, so the tests drive the
// worker off its own writes rather than off a sleep.
func waitBeat(t *testing.T, beats <-chan struct{}) {
	t.Helper()

	select {
	case <-beats:
	case <-time.After(beatTimeout):
		t.Fatal("the worker wrote no heartbeat within the timeout")
	}
}

// Terminating writes the final status and ends the run, after the opening
// in-progress write.
func TestWorker_Run_Terminate(t *testing.T) {
	// Arrange
	writer := &mockHeartbeatWriter{}
	beats := make(chan struct{}, 1)
	opening := writer.On("Write", mock.Anything, StatusInProgress).
		Run(func(mock.Arguments) { beats <- struct{}{} }).Return(nil).Once()
	writer.On("Write", mock.Anything, StatusInProgress).Return(nil).Maybe()
	terminal := writer.On("Write", mock.Anything, StatusDone).Return(nil).Once()
	mock.InOrder(opening, terminal)

	worker := NewWorker(writer).SetInterval(testInterval)
	wg, ctx := errgroup.WithContext(context.Background())

	// Act
	wg.Go(worker.Run(ctx))
	waitBeat(t, beats)
	worker.Terminate(StatusDone)
	err := wg.Wait()

	// Assert
	require.NoError(t, err)
	writer.AssertExpectations(t)
}

// The ticker keeps refreshing the in-progress status for as long as the run
// lasts — that is what makes a stopped run readable as stale.
func TestWorker_Run_Ticks(t *testing.T) {
	// Arrange: the first write is the opening one, the next two come off the ticker.
	writer := &mockHeartbeatWriter{}
	beats := make(chan struct{}, 3)
	writer.On("Write", mock.Anything, StatusInProgress).
		Run(func(mock.Arguments) { beats <- struct{}{} }).Return(nil).Times(3)
	writer.On("Write", mock.Anything, StatusInProgress).Return(nil).Maybe()
	writer.On("Write", mock.Anything, StatusDone).Return(nil).Once()

	worker := NewWorker(writer).SetInterval(testInterval)
	wg, ctx := errgroup.WithContext(context.Background())

	// Act
	wg.Go(worker.Run(ctx))
	for range 3 {
		waitBeat(t, beats)
	}
	worker.Terminate(StatusDone)
	err := wg.Wait()

	// Assert
	require.NoError(t, err)
	writer.AssertExpectations(t)
}

// A cancelled context ends the run without a terminal write: the process is
// going away, and claiming an outcome it never reached would be a lie. The
// writer expects no terminal status, so one would fail the mock.
func TestWorker_Run_ContextCancelled(t *testing.T) {
	// Arrange
	writer := &mockHeartbeatWriter{}
	beats := make(chan struct{}, 1)
	writer.On("Write", mock.Anything, StatusInProgress).
		Run(func(mock.Arguments) { beats <- struct{}{} }).Return(nil).Once()
	writer.On("Write", mock.Anything, StatusInProgress).Return(nil).Maybe()

	worker := NewWorker(writer).SetInterval(testInterval)
	ctx, cancel := context.WithCancel(context.Background())
	wg, ctx := errgroup.WithContext(ctx)

	// Act
	wg.Go(worker.Run(ctx))
	waitBeat(t, beats)
	cancel()
	err := wg.Wait()

	// Assert
	require.NoError(t, err)
	writer.AssertExpectations(t)
}

// The opening write is the run's own precondition: if it fails, the worker never
// starts ticking and the caller hears about it.
func TestWorker_Run_OpeningWriteError(t *testing.T) {
	// Arrange
	writer := &mockHeartbeatWriter{}
	writer.On("Write", mock.Anything, StatusInProgress).Return(errors.New("store boom")).Once()

	// Act
	err := NewWorker(writer).SetInterval(testInterval).Run(context.Background())()

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write status in-progress")
	assert.Contains(t, err.Error(), "store boom")
	writer.AssertExpectations(t)
}

// A tick that cannot be written ends the run too: a heartbeat nobody can refresh
// is indistinguishable from a dead one, so the run stops rather than pretending.
func TestWorker_Run_TickWriteError(t *testing.T) {
	// Arrange
	writer := &mockHeartbeatWriter{}
	opening := writer.On("Write", mock.Anything, StatusInProgress).Return(nil).Once()
	tick := writer.On("Write", mock.Anything, StatusInProgress).Return(errors.New("store boom")).Once()
	mock.InOrder(opening, tick)

	worker := NewWorker(writer).SetInterval(testInterval)
	wg, ctx := errgroup.WithContext(context.Background())

	// Act
	wg.Go(worker.Run(ctx))
	err := wg.Wait()

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handle heartbeat")
	assert.Contains(t, err.Error(), "store boom")
	writer.AssertExpectations(t)
}
