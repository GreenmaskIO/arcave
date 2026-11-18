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

package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// kindSavepointCreate is the root a workflow of core opens outside its locks.
const kindSavepointCreate progress.Kind = "savepoint.create"

var errWorkflow = errors.New("workflow failed")

func newLockTestContext() context.Context {
	return zerolog.Nop().WithContext(context.Background())
}

// interruptedOr reports err as core's workflows report an interrupt: the cause that cancelled ctx
// in its place, err when ctx is live.
func interruptedOr(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("interrupted: %w", context.Cause(ctx))
}

// progressRecorder renders every step's last event as "<path> <status>[ error=…]", in the order
// the steps end; the tracker calls it under its own lock.
type progressRecorder struct {
	paths map[progress.StepID]string
	ended []string
}

func (r *progressRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		segment := string(evt.Open.Step.Kind)
		if id := evt.Open.Step.ID; id != "" {
			segment += "[" + id + "]"
		}
		if parent := r.paths[evt.Open.Parent]; parent != "" {
			segment = parent + "/" + segment
		}
		r.paths[evt.StepID] = segment
	}
	if !evt.Status.IsTerminal() {
		return
	}
	line := r.paths[evt.StepID] + " " + string(evt.Status)
	if evt.Error != "" {
		line += " error=" + evt.Error
	}
	r.ended = append(r.ended, line)
}

func newProgressContext() (context.Context, *progressRecorder) {
	recorder := &progressRecorder{paths: map[progress.StepID]string{}}
	ctx := zerolog.Nop().WithContext(context.Background())
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	return progress.WithTracker(ctx, progress.New(recorder, now)), recorder
}

func TestReadSessionRef(t *testing.T) {
	loadErr := errors.New("store unreachable")
	tests := []struct {
		name    string
		id      model.SessionID
		setup   func(sessions *mockSessionLookup)
		want    model.SessionRef
		wantErr error
	}{
		{
			name: "the default reads no record",
			id:   model.DefaultSession,
			want: model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
		},
		{
			name: "a session is named as its record names it",
			id:   demoSession,
			setup: func(sessions *mockSessionLookup) {
				sessions.On("LoadSession", mock.Anything, demoSession).
					Return(model.Session{ID: demoSession, Name: "exp"}, true, nil).Once()
			},
			want: model.SessionRef{ID: demoSession, Name: "exp"},
		},
		{
			name: "a session with no record is gone",
			id:   demoSession,
			setup: func(sessions *mockSessionLookup) {
				sessions.On("LoadSession", mock.Anything, demoSession).Return(model.Session{}, false, nil).Once()
			},
			wantErr: model.ErrSessionNotFound,
		},
		{
			name: "a record that cannot be read",
			id:   demoSession,
			setup: func(sessions *mockSessionLookup) {
				sessions.On("LoadSession", mock.Anything, demoSession).Return(model.Session{}, false, loadErr).Once()
			},
			wantErr: loadErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			sessions := &mockSessionLookup{}
			if tt.setup != nil {
				tt.setup(sessions)
			}

			// Act
			got, err := ReadSessionRef(context.Background(), sessions, tt.id)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, model.SessionRef{}, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			sessions.AssertExpectations(t)
		})
	}
}

func TestWithSessionLock(t *testing.T) {
	tests := []struct {
		name string
		// lockErr fails the Lock; lost cancels the context it returns with ErrSessionLockLost.
		lockErr error
		lost    bool
		// fnErr fails the workflow; interrupted reports it as a workflow reports an interrupt.
		fnErr       error
		interrupted bool
		freeErr     error
		want        string
		wantErr     error
	}{
		{
			name: "the workflow runs under the lock and frees it",
			want: "done",
		},
		{
			name:    "a session held past the wait runs nothing",
			lockErr: model.ErrSessionLocked,
			wantErr: model.ErrSessionLocked,
		},
		{
			name:        "a workflow interrupted by the lost lock reports the lost lock",
			lost:        true,
			fnErr:       errWorkflow,
			interrupted: true,
			wantErr:     model.ErrSessionLockLost,
		},
		{
			name:    "a workflow failing on its own after the lock is lost reports its own failure",
			lost:    true,
			fnErr:   errWorkflow,
			wantErr: errWorkflow,
		},
		{
			name: "a workflow finishing after the lock is lost keeps its result",
			lost: true,
			want: "done",
		},
		{
			name:    "a workflow failing on its own reports its own failure",
			fnErr:   errWorkflow,
			wantErr: errWorkflow,
		},
		{
			name:    "a failed unlock is only logged",
			freeErr: errors.New("store hiccup"),
			want:    "done",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			if tt.lost {
				grantedLock.lostCause = model.ErrSessionLockLost
			}
			var ranWith model.GrantedSessionLock
			lock := locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
				Return(grantedLock, tt.lockErr).Once()
			if tt.lockErr == nil {
				unlock := grantedLock.On("Unlock", mock.Anything).Return(tt.freeErr).Once().
					Run(func(mock.Arguments) { assert.Same(t, grantedLock, ranWith, "the workflow ran before the unlock") })
				mock.InOrder(lock, unlock)
			}

			// Act
			got, err := WithSessionLock(newLockTestContext(), locker, model.SessionLockOperationSavepointCreate,
				func(ctx context.Context, grantedLock model.GrantedSessionLock) (string, error) {
					ranWith = grantedLock
					if tt.interrupted {
						return "", interruptedOr(ctx, tt.fnErr)
					}
					if tt.fnErr != nil {
						return "", tt.fnErr
					}
					return "done", nil
				})

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.lockErr == nil, ranWith != nil, "the workflow runs only under the lock")
			locker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}

// A lost lock cancels the work under it, and the root, opened outside the lock, carries why.
func TestWithSessionLock_LostLockCancelsTheWork(t *testing.T) {
	// Arrange
	ctx, recorder := newProgressContext()
	locker := &mockSessionLocker{}
	grantedLock := &mockGrantedSessionLock{lostCause: model.ErrSessionLockLost}
	locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).Return(grantedLock, nil).Once()
	grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
	ctx, root := progress.Begin(ctx, progress.Step{Kind: kindSavepointCreate})

	// Act
	_, err := WithSessionLock(ctx, locker, model.SessionLockOperationSavepointCreate,
		func(ctx context.Context, _ model.GrantedSessionLock) (string, error) {
			_, step := progress.Run(ctx, progress.Step{Kind: "stage.teardown"})
			err := interruptedOr(ctx, errWorkflow)
			step.End(err)
			return "", err
		})
	root.End(err)

	// Assert
	require.ErrorIs(t, err, model.ErrSessionLockLost)
	require.Len(t, recorder.ended, 2)
	assert.Equal(t, "savepoint.create/stage.teardown cancelled", recorder.ended[0])
	assert.True(t, strings.HasPrefix(recorder.ended[1], "savepoint.create failed error=interrupted: "),
		"the cause is on the root: %s", recorder.ended[1])
	assert.Contains(t, recorder.ended[1], model.ErrSessionLockLost.Error())
	locker.AssertExpectations(t)
	grantedLock.AssertExpectations(t)
}

// The lock's step is shown only once the locker waits on another holder.
func TestWithSessionLock_ShowsTheWaitOnlyWhenHeld(t *testing.T) {
	tests := []struct {
		name string
		held bool
		want []string
	}{
		{
			name: "a free lock shows nothing, so the lazy root never opens",
		},
		{
			name: "a held lock shows the wait",
			held: true,
			want: []string{"savepoint.create/session.lock succeeded", "savepoint.create succeeded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, recorder := newProgressContext()
			locker := &mockSessionLocker{}
			grantedLock := &mockGrantedSessionLock{}
			locker.On("Lock", mock.Anything, model.SessionLockOperationSavepointCreate).
				Run(func(args mock.Arguments) {
					if tt.held {
						progress.Current(args.Get(0).(context.Context)).Warn("waiting: held by rollback")
					}
				}).
				Return(grantedLock, nil).Once()
			grantedLock.On("Unlock", mock.Anything).Return(nil).Once()
			ctx, root := progress.Begin(ctx, progress.Step{Kind: kindSavepointCreate})

			// Act
			_, err := WithSessionLock(ctx, locker, model.SessionLockOperationSavepointCreate,
				func(context.Context, model.GrantedSessionLock) (string, error) { return "", nil })
			root.End(err)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, recorder.ended)
			locker.AssertExpectations(t)
			grantedLock.AssertExpectations(t)
		})
	}
}
