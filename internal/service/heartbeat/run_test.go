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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var (
	errVerify = errors.New("lock lost")
	errCommit = errors.New("store unreachable")
)

// startRun starts a run whose beats never tick on their own, its first beat expected on w.
func startRun(t *testing.T, ctx context.Context, w *mockWriter) (context.Context, *Run) {
	t.Helper()
	w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(nil).Once()
	beatCtx, run, err := StartRun(ctx, w, time.Hour, 2*time.Hour, testBase)
	require.NoError(t, err)
	return beatCtx, run
}

// The tail runs stop, alive, verify, commit and done in order; a refusal stops it there and marks
// nothing, for the caller to hand to Fail.
func TestRun_Commit(t *testing.T) {
	tests := []struct {
		name      string
		stale     bool
		verifyErr error
		commitErr error
		wantCalls []string
		wantErr   error
	}{
		{
			name:      "work that succeeded is committed and marked done",
			wantCalls: []string{"verify", "commit"},
		},
		{
			name:    "a stale heartbeat refuses before the verify",
			stale:   true,
			wantErr: model.ErrHeartbeatLost,
		},
		{
			name:      "a failed verify commits nothing",
			verifyErr: errVerify,
			wantCalls: []string{"verify"},
			wantErr:   errVerify,
		},
		{
			name:      "a failed commit marks nothing",
			commitErr: errCommit,
			wantCalls: []string{"verify", "commit"},
			wantErr:   errCommit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			w := &mockWriter{}
			_, run := startRun(t, ctx, w)
			if tt.wantErr == nil {
				w.On("Write", mock.Anything, beatOf(StatusDone, "")).Return(nil).Once()
			}
			if tt.stale {
				run.worker.mu.Lock()
				run.worker.last = wallClock().Add(-2 * time.Hour)
				run.worker.mu.Unlock()
			}
			var calls []string
			verify := func(context.Context) error {
				calls = append(calls, "verify")
				return tt.verifyErr
			}
			commit := func(context.Context) error {
				calls = append(calls, "commit")
				return tt.commitErr
			}

			// Act
			err := run.Commit(ctx, verify, commit)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, calls)
			w.AssertExpectations(t)
		})
	}
}

// A commit that lands before an interrupt is marked done past it: in progress would read as live.
func TestRun_Commit_MarksDonePastAnInterrupt(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
	w := &mockWriter{}
	beatCtx, run := startRun(t, ctx, w)
	w.On("Write", mock.MatchedBy(func(c context.Context) bool { return c.Err() == nil }),
		beatOf(StatusDone, "")).Return(nil).Once()
	commit := func(context.Context) error {
		cancel()
		return nil
	}

	// Act
	err := run.Commit(ctx, func(context.Context) error { return nil }, commit)

	// Assert
	require.NoError(t, err)
	assert.ErrorIs(t, beatCtx.Err(), context.Canceled, "the run's context is closed once done")
	w.AssertExpectations(t)
}

// Fail marks the heartbeat failed with the cause it is given, whether the beats still run or a
// refused Commit has stopped them.
func TestRun_Fail(t *testing.T) {
	tests := []struct {
		name          string
		refusedCommit bool
	}{
		{name: "work that failed"},
		{name: "a refused commit", refusedCommit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			w := &mockWriter{}
			beatCtx, run := startRun(t, ctx, w)
			w.On("Write", mock.Anything, beatOf(StatusFailed, "copy: boom")).Return(nil).Once()
			if tt.refusedCommit {
				require.ErrorIs(t, run.Commit(ctx, func(context.Context) error { return errVerify }, nil), errVerify)
			}

			// Act
			run.Fail(ctx, errors.New("copy: boom"))

			// Assert
			assert.ErrorIs(t, beatCtx.Err(), context.Canceled, "the run's context is closed once failed")
			w.AssertExpectations(t)
		})
	}
}

// A first beat that fails may still have landed: the start marks it failed.
func TestStartRun_FirstWriteFails(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	w := &mockWriter{}
	first := w.On("Write", mock.Anything, beatOf(StatusInProgress, "")).Return(errStore).Once()
	failed := w.On("Write", mock.Anything, mock.MatchedBy(func(h Heartbeat) bool {
		return h.Status == StatusFailed && strings.Contains(h.Error, errStore.Error())
	})).Return(nil).Once()
	mock.InOrder(first, failed)

	// Act
	beatCtx, run, err := StartRun(ctx, w, time.Hour, 2*time.Hour, testBase)

	// Assert
	require.ErrorIs(t, err, model.ErrHeartbeatLost)
	assert.Nil(t, beatCtx)
	assert.Nil(t, run)
	w.AssertExpectations(t)
}

func TestPreferLost(t *testing.T) {
	errSymptom := errors.New("copy: context canceled")
	tests := []struct {
		name  string
		cause error
		want  error
	}{
		{name: "a live context keeps the error", want: errSymptom},
		{name: "a lost heartbeat stands in for its symptom", cause: model.ErrHeartbeatLost, want: model.ErrHeartbeatLost},
		{name: "another cancellation keeps the error", cause: context.Canceled, want: errSymptom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			beatCtx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if tt.cause != nil {
				cancel(tt.cause)
			}

			// Act
			got := PreferLost(beatCtx, errSymptom)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
