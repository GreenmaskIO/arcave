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
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newMockCaptureTriggers returns n strict mock triggers, as mocks and as the triggers they form.
func newMockCaptureTriggers(n int) ([]*mockCaptureTrigger, captureTriggers) {
	mocks := make([]*mockCaptureTrigger, n)
	triggers := make(captureTriggers, n)
	for i := range n {
		mocks[i] = &mockCaptureTrigger{}
		triggers[i] = mocks[i]
	}
	return mocks, triggers
}

func TestCaptureTriggers_beforeCapture(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name string
		// errs fail each trigger's beforeCapture; a trigger past the first failure is not called.
		errs       []error
		wantKilled []string
		wantErr    string
	}{
		{
			name:       "every trigger in order, killed joined",
			errs:       []error{nil, nil, nil},
			wantKilled: []string{"c0", "c1", "c2"},
		},
		{
			name:    "a failure in the middle stops the rest",
			errs:    []error{nil, errBoom, nil},
			wantErr: "boom",
		},
		{
			name:    "a failure of the first stops every other",
			errs:    []error{errBoom, nil, nil},
			wantErr: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			mocks, triggers := newMockCaptureTriggers(len(tt.errs))
			var calls []*mock.Call
			for i, err := range tt.errs {
				calls = append(calls, mocks[i].On("beforeCapture", mock.Anything).
					Return([]string{"c" + strconv.Itoa(i)}, err).Once())
				if err != nil {
					break
				}
			}
			mock.InOrder(calls...)

			// Act
			killed, err := triggers.beforeCapture(context.Background())

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantKilled, killed)
			for _, trigger := range mocks {
				trigger.AssertExpectations(t)
			}
		})
	}
}

func TestCaptureTriggers_afterCapture(t *testing.T) {
	tests := []struct {
		name string
		// errs fail each trigger's afterCapture.
		errs    []error
		wantErr string
		// wantLog is logged; wantNoLog is not.
		wantLog   []string
		wantNoLog []string
	}{
		{
			name:      "every trigger, last to first",
			errs:      []error{nil, nil, nil},
			wantNoLog: []string{"capture trigger failed after the capture"},
		},
		{
			name:      "a failure runs the rest and the first is returned",
			errs:      []error{errors.New("first"), nil, errors.New("last")},
			wantErr:   "last",
			wantLog:   []string{"capture trigger failed after the capture", "first"},
			wantNoLog: []string{"last"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())
			mocks, triggers := newMockCaptureTriggers(len(tt.errs))
			calls := make([]*mock.Call, len(tt.errs))
			for i, err := range tt.errs {
				calls[len(tt.errs)-1-i] = mocks[i].On("afterCapture", mock.Anything).Return(err).Once()
			}
			mock.InOrder(calls...)

			// Act
			err := triggers.afterCapture(ctx)

			// Assert
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
			for _, line := range tt.wantLog {
				assert.Contains(t, logs.String(), line)
			}
			for _, line := range tt.wantNoLog {
				assert.NotContains(t, logs.String(), line)
			}
			for _, trigger := range mocks {
				trigger.AssertExpectations(t)
			}
		})
	}
}

// A failed beforeCapture of the second still runs every trigger's afterCapture, last to first.
func TestCaptureTriggers_afterCapture_AfterAFailedBefore(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mocks, triggers := newMockCaptureTriggers(3)
	mock.InOrder(
		mocks[0].On("beforeCapture", mock.Anything).Return(nil, nil).Once(),
		mocks[1].On("beforeCapture", mock.Anything).Return(nil, errors.New("boom")).Once(),
		mocks[2].On("afterCapture", mock.Anything).Return(nil).Once(),
		mocks[1].On("afterCapture", mock.Anything).Return(nil).Once(),
		mocks[0].On("afterCapture", mock.Anything).Return(nil).Once(),
	)

	// Act
	_, beforeErr := triggers.beforeCapture(ctx)
	afterErr := triggers.afterCapture(ctx)

	// Assert
	assert.EqualError(t, beforeErr, "boom")
	assert.NoError(t, afterErr)
	for _, trigger := range mocks {
		trigger.AssertExpectations(t)
	}
}

func TestStopTrigger_beforeCapture(t *testing.T) {
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
			trigger := stopTrigger{composeRunner: runner, project: demoSpec, waitTimeout: time.Minute}

			// Act
			killed, err := trigger.beforeCapture(context.Background())

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

func TestStopTrigger_afterCapture(t *testing.T) {
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
			runner.On("Start", mock.Anything, demoSpec, time.Minute).Return(tt.startErr).Once()
			trigger := stopTrigger{composeRunner: runner, project: demoSpec, waitTimeout: time.Minute}

			// Act
			err := trigger.afterCapture(context.Background())

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

func TestNewCaptureTriggers(t *testing.T) {
	// Arrange
	runner := &mockComposeRunner{}
	plan := Plan{Project: demoSpec, WaitTimeout: time.Minute}

	// Act
	triggers := newCaptureTriggers(runner, plan)

	// Assert
	assert.Equal(t, captureTriggers{
		stopTrigger{composeRunner: runner, project: demoSpec, waitTimeout: time.Minute},
	}, triggers)
}
