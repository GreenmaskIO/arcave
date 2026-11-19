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

package core

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/provider"
)

// A savepoint's capture is planned off the inspected workload with no fact of a restore.
func TestService_prepare(t *testing.T) {
	// Arrange
	stages := newMockStages(false)
	mock.InOrder(
		stages.expectInspect(nil),
		stages.expectPlanCapture(provider.CaptureOptions{}, nil),
	)
	svc := newStagedService(t, stages, &mockSavepoints{}, &mockLocker{}, &fakeInflight{})

	// Act
	plan, err := svc.prepare(zerolog.Nop().WithContext(context.Background()))

	// Assert
	require.NoError(t, err)
	assert.Same(t, &stages.plan, plan)
	stages.AssertExpectations(t)
}

// A workload that cannot be inspected — a daemon refused among the reasons — plans nothing.
func TestService_prepare_AnInspectionFailurePlansNothing(t *testing.T) {
	// Arrange
	stages := newMockStages(false)
	stages.expectInspect(errUnsupportedDaemon)
	svc := newStagedService(t, stages, &mockSavepoints{}, &mockLocker{}, &fakeInflight{})

	// Act
	plan, err := svc.prepare(zerolog.Nop().WithContext(context.Background()))

	// Assert
	require.ErrorIs(t, err, errUnsupportedDaemon)
	assert.Nil(t, plan)
	stages.AssertExpectations(t)
}
