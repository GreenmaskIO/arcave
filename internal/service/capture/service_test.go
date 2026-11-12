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

package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestService_Run(t *testing.T) {
	errBoom := errors.New("boom")
	killed := []string{"c1"}

	tests := []struct {
		name string
		// stopErr fails the stop; snapshotErr the volume's capture; startErr the start.
		stopErr     error
		snapshotErr error
		startErr    error
		// interruptStop, interruptCapture and interruptCommit cancel the command's context
		// inside the stop, the volume's capture and the record's commit.
		interruptStop    bool
		interruptCapture bool
		interruptCommit  bool
		// wantCommitted says the savepoint is returned; wantErr names its id as {id}.
		wantCommitted bool
		wantErr       string
		// wantLogOrder are lines the log must hold in this order; "compose start" is the start's.
		wantLogOrder []string
		// wantNoRestartLog says no start failure was logged.
		wantNoRestartLog bool
	}{
		{
			name:             "stop, capture, start",
			wantCommitted:    true,
			wantNoRestartLog: true,
		},
		{
			name:             "a failed stop starts the project and captures nothing",
			stopErr:          errBoom,
			wantErr:          "stopping services: boom",
			wantNoRestartLog: true,
		},
		{
			name:          "an interrupted stop starts the project and captures nothing",
			stopErr:       errBoom,
			interruptStop: true,
			wantErr:       "interrupted: context canceled",
			wantLogOrder: []string{
				"savepoint interrupted while stopping services",
				"compose start",
				"failure the interrupt caused",
			},
			wantNoRestartLog: true,
		},
		{
			name:             "a failed capture starts the project",
			snapshotErr:      errBoom,
			wantErr:          "capturing volumes: snapshot volume-named-pgdata: capture volume: boom",
			wantNoRestartLog: true,
		},
		{
			name:             "an interrupted capture starts the project on a live context",
			snapshotErr:      errBoom,
			interruptCapture: true,
			wantErr:          "capturing volumes: interrupted: context canceled",
			wantLogOrder: []string{
				"savepoint interrupted during capture",
				"failure the interrupt caused",
				"compose start",
			},
			wantNoRestartLog: true,
		},
		{
			name:             "an interrupt past the commit starts the project and fails nothing",
			interruptCommit:  true,
			wantCommitted:    true,
			wantLogOrder:     []string{"savepoint committed before the interrupt", "compose start"},
			wantNoRestartLog: true,
		},
		{
			name:             "a failed start past the commit names the savepoint",
			startErr:         errBoom,
			wantErr:          "savepoint {id} committed; services failed to start: starting services: boom",
			wantNoRestartLog: true,
		},
		{
			name:         "a failed start under a failed capture is logged",
			snapshotErr:  errors.New("capture boom"),
			startErr:     errBoom,
			wantErr:      "capturing volumes: snapshot volume-named-pgdata: capture volume: capture boom",
			wantLogOrder: []string{"services failed to restart after capture failure"},
		},
		{
			name:         "a failed start under a failed stop is logged",
			stopErr:      errors.New("stop boom"),
			startErr:     errBoom,
			wantErr:      "stopping services: stop boom",
			wantLogOrder: []string{"services failed to restart after stop failure"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(zerolog.New(logs).WithContext(context.Background()))
			defer cancel()
			logger := zerolog.Ctx(ctx)
			// The start runs on a revertContext: live, bounded by the revert budget, logging as ctx does.
			revertCtx := mock.MatchedBy(func(ctx context.Context) bool {
				_, bounded := ctx.Deadline()
				return ctx.Err() == nil && bounded && zerolog.Ctx(ctx) == logger
			})
			interruptIf := func(interrupt bool) func(mock.Arguments) {
				return func(mock.Arguments) {
					if interrupt {
						cancel()
					}
				}
			}

			runner := &mockComposeRunner{}
			backend := &mockBackend{volume: demoVolume()}
			repo := &mockSavepointRepo{}
			var id model.SavepointID
			stop := runner.On("Stop", mock.Anything, demoSpec).
				Run(interruptIf(tt.interruptStop)).
				Return(killed, tt.stopErr).Once()
			start := runner.On("Start", revertCtx, demoSpec, time.Minute).
				Run(func(args mock.Arguments) {
					zerolog.Ctx(args.Get(0).(context.Context)).Info().Msg("compose start")
				}).
				Return(tt.startErr).Once()
			calls := []*mock.Call{stop}
			if tt.stopErr == nil {
				calls = append(calls, backend.On("Snapshot", mock.Anything, matchValidSnapshotID()).
					Run(interruptIf(tt.interruptCapture)).
					Return(model.SnapshotStat{Volume: demoVolume(), Type: model.SnapshotBackendFullCopy}, tt.snapshotErr).
					Once())
			}
			if tt.stopErr == nil && tt.snapshotErr == nil {
				calls = append(calls,
					expectSpecCommit(repo),
					repo.On("SaveSavepoint", mock.Anything, mock.MatchedBy(func(savepoint model.Savepoint) bool {
						return savepoint.ID.Validate() == nil && savepoint.Name == "sp"
					})).
						Run(func(args mock.Arguments) {
							id = args.Get(1).(model.Savepoint).ID
							interruptIf(tt.interruptCommit)(args)
						}).
						Return(nil).Once(),
				)
			}
			mock.InOrder(append(calls, start)...)
			svc := New(Deps{
				SavepointRepo: repo,
				ComposeRunner: runner,
				Store:         newDirStore(t),
			})
			plan := Plan{
				Project:     demoSpec,
				Spec:        demoSpecBytes,
				WaitTimeout: time.Minute,
				Volumes:     []model.VolumeSnapshotBackend{backend},
			}

			// Act
			savepoint, err := svc.Run(ctx, fakeGrantedSessionLock{}, plan, Request{Name: "sp"})

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, strings.ReplaceAll(tt.wantErr, "{id}", id.String()))
			}
			if tt.wantCommitted {
				assert.Equal(t, id, savepoint.ID)
				assert.Equal(t, "sp", savepoint.Name)
			} else {
				assert.Zero(t, savepoint)
			}
			assertLogOrder(t, logs.String(), tt.wantLogOrder)
			if tt.wantNoRestartLog {
				assert.NotContains(t, logs.String(), "services failed to restart")
			}
			runner.AssertExpectations(t)
			backend.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

func TestService_Hold(t *testing.T) {
	tests := []struct {
		name       string
		stopErr    error
		wantKilled []string
		wantErr    string
	}{
		{
			name:       "stops the project",
			wantKilled: []string{"c1"},
		},
		{
			name:    "a failed stop",
			stopErr: errors.New("boom"),
			wantErr: "stopping services: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockComposeRunner{}
			runner.On("Stop", mock.Anything, demoSpec).Return([]string{"c1"}, tt.stopErr).Once()
			svc := New(Deps{ComposeRunner: runner})

			// Act
			killed, err := svc.Hold(context.Background(), Plan{Project: demoSpec, WaitTimeout: time.Minute})

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantKilled, killed)
			runner.AssertExpectations(t)
		})
	}
}

// Release starts the project on a live context even when the one it is handed was cancelled.
func TestService_Release(t *testing.T) {
	tests := []struct {
		name     string
		startErr error
		wantErr  string
	}{
		{
			name: "starts the project",
		},
		{
			name:     "a failed start",
			startErr: errors.New("boom"),
			wantErr:  "starting services: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			runner := &mockComposeRunner{}
			revertCtx := mock.MatchedBy(func(ctx context.Context) bool {
				_, bounded := ctx.Deadline()
				return ctx.Err() == nil && bounded
			})
			runner.On("Start", revertCtx, demoSpec, time.Minute).Return(tt.startErr).Once()
			svc := New(Deps{ComposeRunner: runner})
			ctx, cancel := context.WithCancel(zerolog.Nop().WithContext(context.Background()))
			cancel()

			// Act
			err := svc.Release(ctx, Plan{Project: demoSpec, WaitTimeout: time.Minute})

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			runner.AssertExpectations(t)
		})
	}
}
