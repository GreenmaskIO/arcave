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
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

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
