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

package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// fastInterval paces the beats of a test that waits for ticks.
const fastInterval = time.Millisecond

var (
	testRunnerInfo = RunnerInfo{Host: "box", PID: 42, ArcaveVersion: "dev"}
	testBase       = Heartbeat{Operation: "fork", RunnerInfo: testRunnerInfo, Data: json.RawMessage(`{"from":"x"}`)}
	errStore       = errors.New("store hiccup")
)

type mockWriter struct{ mock.Mock }

func (m *mockWriter) Write(ctx context.Context, h Heartbeat) error {
	return m.Called(ctx, h).Error(0)
}

// beatOf matches a heartbeat of testBase's operation written with status and cause.
func beatOf(status Status, cause string) any {
	return mock.MatchedBy(func(h Heartbeat) bool {
		return h.Operation == testBase.Operation && bytes.Equal(h.Data, testBase.Data) &&
			h.RunnerInfo == testRunnerInfo && h.Status == status && h.Error == cause &&
			!h.StartedAt.IsZero() && !h.UpdatedAt.Before(h.StartedAt) && h.UpdatedAt.Location() == time.UTC &&
			(status == StatusInProgress) == h.FinishedAt.IsZero()
	})
}

// signal reports each write on wrote.
func signal(wrote chan<- struct{}) func(mock.Arguments) {
	return func(mock.Arguments) { wrote <- struct{}{} }
}

// startWorker starts a worker beating every interval and cancelling once deadline is reached.
func startWorker(
	t *testing.T, ctx context.Context, w *mockWriter, interval, deadline time.Duration, cancel context.CancelCauseFunc,
) *Worker {
	t.Helper()
	hb := NewWorker(w).SetInterval(interval).SetStaleAfter(interval + deadline)
	require.NoError(t, hb.Start(ctx, testBase, cancel))
	return hb
}

func TestNewWorker(t *testing.T) {
	// Act
	hb := NewWorker(&mockWriter{})

	// Assert
	assert.Equal(t, DefaultInterval, hb.interval)
	assert.Equal(t, DefaultStaleAfter-DefaultInterval, hb.Deadline(),
		"the tick that reaches the deadline comes before any gc sees the tree stale")
}

func TestWorker_Start(t *testing.T) {
	// Arrange
	w := &mockWriter{}
	w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()

	// Act
	hb := NewWorker(w)
	require.NoError(t, hb.Start(context.Background(), testBase, func(error) {}))
	hb.Stop()

	// Assert
	w.AssertExpectations(t)
}

func TestWorker_Start_FirstWriteFails(t *testing.T) {
	// Arrange
	w := &mockWriter{}
	w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(errStore).Once()

	// Act
	err := NewWorker(w).Start(context.Background(), testBase, func(error) {})

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatLost)
	require.ErrorIs(t, err, errStore)
	w.AssertExpectations(t)
}

func TestWorker_run_WritesOnEveryTick(t *testing.T) {
	// Arrange
	wrote := make(chan struct{})
	w := &mockWriter{}
	first := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
	ticked := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).
		Run(signal(wrote)).Return(nil).Twice()
	mock.InOrder(first, ticked)
	opCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	hb := startWorker(t, context.Background(), w, fastInterval, time.Hour, cancel)

	// Act
	<-wrote
	<-wrote
	hb.Stop()

	// Assert
	assert.NoError(t, context.Cause(opCtx))
	w.AssertExpectations(t)
}

func TestWorker_run_FailedWriteIsRetried(t *testing.T) {
	// Arrange: the first tick's write fails, the next lands and moves the deadline.
	wrote := make(chan struct{})
	w := &mockWriter{}
	first := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
	failed := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).
		Run(signal(wrote)).Return(errStore).Once()
	retried := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).
		Run(signal(wrote)).Return(nil).Once()
	mock.InOrder(first, failed, retried)
	opCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	hb := startWorker(t, context.Background(), w, fastInterval, time.Hour, cancel)

	// Act
	<-wrote
	<-wrote
	hb.Stop()

	// Assert
	assert.NoError(t, context.Cause(opCtx), "a failed write within the deadline cancels nothing")
	assert.NoError(t, hb.Alive())
	w.AssertExpectations(t)
}

func TestWorker_run_FailedWriteKeepsTheDeadline(t *testing.T) {
	// Arrange
	wrote := make(chan struct{})
	var good time.Time
	w := &mockWriter{}
	first := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).
		Run(func(args mock.Arguments) { good = args.Get(1).(Heartbeat).UpdatedAt }).Return(nil).Once()
	failed := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).
		Run(signal(wrote)).Return(errStore).Once()
	mock.InOrder(first, failed)
	hb := startWorker(t, context.Background(), w, fastInterval, time.Hour, func(error) {})

	// Act
	<-wrote
	hb.Stop()

	// Assert: measured from the last good beat, not from the failed one.
	assert.Equal(t, good, hb.last)
	w.AssertExpectations(t)
}

func TestWorker_run_TickPastTheDeadlineCancels(t *testing.T) {
	// Arrange: strict mock, so a write on the late tick fails the test.
	w := &mockWriter{}
	w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
	opCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	// Act
	hb := startWorker(t, context.Background(), w, fastInterval, 0, cancel)
	<-hb.done

	// Assert
	require.ErrorIs(t, context.Cause(opCtx), model.ErrHeartbeatLost)
	w.AssertExpectations(t)
}

func TestWorker_run_EndsWithoutWriting(t *testing.T) {
	tests := []struct {
		name string
		end  func(hb *Worker, stopCtx context.CancelFunc)
	}{
		{name: "stop", end: func(hb *Worker, _ context.CancelFunc) { hb.Stop() }},
		{name: "context done", end: func(hb *Worker, stopCtx context.CancelFunc) {
			stopCtx()
			<-hb.done
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: strict mock, so any write past the first fails the test.
			w := &mockWriter{}
			w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
			ctx, stopCtx := context.WithCancel(context.Background())
			defer stopCtx()
			hb := startWorker(t, ctx, w, time.Hour, time.Hour, func(error) {})

			// Act
			tt.end(hb, stopCtx)

			// Assert
			w.AssertExpectations(t)
		})
	}
}

func TestWorker_Alive(t *testing.T) {
	tests := []struct {
		name    string
		age     time.Duration
		wantErr bool
	}{
		{name: "fresh", age: 0},
		{name: "a minute short of the deadline", age: DefaultStaleAfter - DefaultInterval - time.Minute},
		{name: "at the deadline", age: DefaultStaleAfter - DefaultInterval, wantErr: true},
		{name: "past the deadline", age: DefaultStaleAfter - DefaultInterval + time.Minute, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			hb := NewWorker(&mockWriter{})
			hb.last = wallClock().Add(-tt.age)

			// Act
			err := hb.Alive()

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, model.ErrHeartbeatLost)
				assert.ErrorContains(t, err, "last written")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestWorker_Finish(t *testing.T) {
	tests := []struct {
		name     string
		writeErr error
		wantLog  string
	}{
		{name: "written"},
		{name: "a failed write is only logged", writeErr: errStore, wantLog: "marking the operation done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var logs bytes.Buffer
			ctx := zerolog.New(&logs).WithContext(context.Background())
			w := &mockWriter{}
			first := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
			done := w.On("Write", mock.Anything, beatOf(StatusDone, "")).Return(tt.writeErr).Once()
			mock.InOrder(first, done)
			hb := startWorker(t, ctx, w, time.Hour, time.Hour, func(error) {})
			hb.Stop()

			// Act
			hb.Finish(ctx)

			// Assert
			if tt.wantLog == "" {
				assert.Empty(t, logs.String())
			} else {
				assert.Contains(t, logs.String(), tt.wantLog)
			}
			w.AssertExpectations(t)
		})
	}
}

func TestWorker_Fail(t *testing.T) {
	tests := []struct {
		name     string
		writeErr error
		wantLog  string
	}{
		{name: "written past a cancelled context"},
		{name: "a failed write is only logged", writeErr: errStore, wantLog: "marking the operation failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: the command is cancelled before the mark.
			var logs bytes.Buffer
			ctx, stopCtx := context.WithCancel(zerolog.New(&logs).WithContext(context.Background()))
			w := &mockWriter{}
			first := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
			failed := w.On("Write", mock.MatchedBy(func(c context.Context) bool { return c.Err() == nil }),
				beatOf(StatusFailed, "copy: boom")).Return(tt.writeErr).Once()
			mock.InOrder(first, failed)
			hb := startWorker(t, ctx, w, time.Hour, time.Hour, func(error) {})
			hb.Stop()
			stopCtx()

			// Act
			hb.Fail(ctx, errors.New("copy: boom"))

			// Assert
			if tt.wantLog == "" {
				assert.Empty(t, logs.String())
			} else {
				assert.Contains(t, logs.String(), tt.wantLog)
			}
			w.AssertExpectations(t)
		})
	}
}

func TestWorker_beat_WriteBudget(t *testing.T) {
	// Arrange
	w := &mockWriter{}
	w.On("Write", mock.MatchedBy(func(c context.Context) bool {
		deadline, ok := c.Deadline()
		return ok && time.Until(deadline) <= WriteTimeout
	}), beatOf(StatusInProgress, "")).Return(nil).Once()

	// Act
	hb := startWorker(t, context.Background(), w, time.Hour, time.Hour, func(error) {})
	hb.Stop()

	// Assert
	w.AssertExpectations(t)
}

func TestWallClock(t *testing.T) {
	// Act
	got := wallClock()

	// Assert
	assert.Equal(t, time.UTC, got.Location())
	assert.Equal(t, got, got.Round(0), "no monotonic reading")
}
