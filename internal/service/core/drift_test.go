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
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	keptStamp     = "true; backend=fullcopy; origin=config"
	excludedStamp = "false; origin=label; source=arcave.mounts.exclude=/cache on service app"
)

// stampedProject is one service, app, carrying the stamps given.
func stampedProject(labels types.Labels) *types.Project {
	return &types.Project{Services: types.Services{"app": {Name: "app", Labels: labels}}}
}

func TestFindCapturePolicyDrift(t *testing.T) {
	cache := model.NewMountStampLabel("/cache")
	tests := []struct {
		name          string
		previous, now types.Labels
		want          []capturePolicyChange
	}{
		{name: "the same decision is no drift",
			previous: types.Labels{string(cache): keptStamp}, now: types.Labels{string(cache): keptStamp}},
		{name: "a unit left out since",
			previous: types.Labels{string(cache): keptStamp}, now: types.Labels{string(cache): excludedStamp},
			want: []capturePolicyChange{{service: "app", stampKey: string(cache), previous: keptStamp, current: excludedStamp}}},
		{name: "a backend changed since",
			previous: types.Labels{string(cache): keptStamp}, now: types.Labels{string(cache): "true; backend=cdc; origin=label"},
			want: []capturePolicyChange{{service: "app", stampKey: string(cache), previous: keptStamp, current: "true; backend=cdc; origin=label"}}},
		{name: "only the reason changed is no drift",
			previous: types.Labels{string(cache): "false; origin=config; source=capture.volumes.anonymous=false in the config"},
			now:      types.Labels{string(cache): excludedStamp}},
		{name: "a unit only one spec has is topology",
			previous: types.Labels{}, now: types.Labels{string(cache): excludedStamp}},
		{name: "a stamp that cannot be read claims nothing",
			previous: types.Labels{string(cache): "yes"}, now: types.Labels{string(cache): excludedStamp}},
		{name: "the user's own labels are not compared",
			previous: types.Labels{string(model.CaptureLabelMountsExclude): "/cache"}, now: types.Labels{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := findCapturePolicyDrift(stampedProject(tt.previous), stampedProject(tt.now))

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindLatestSavepoint(t *testing.T) {
	// Arrange
	older := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "older"}
	newer := model.Savepoint{ID: "019e843f-0000-7000-8000-000000000000", Name: "newer"}

	// Act
	got, ok := findLatestSavepoint([]model.Savepoint{newer, older})
	_, none := findLatestSavepoint(nil)

	// Assert
	require.True(t, ok)
	assert.Equal(t, "newer", got.Name)
	assert.False(t, none)
}

func TestService_warnCapturePolicyDrift(t *testing.T) {
	tests := []struct {
		name     string
		previous *types.Project
		parseErr error
		want     string
	}{
		{name: "a drift is warned of, naming the savepoint",
			previous: stampedProject(types.Labels{string(model.NewMountStampLabel("/cache")): keptStamp}),
			want:     `"message":"captured otherwise than in the latest savepoint"`},
		{name: "a spec that cannot be read is only logged",
			parseErr: errors.New("broken"),
			want:     `"message":"cannot compare the capture policy with the latest savepoint"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			record := demoRecord()
			repo := &mockSavepointRepo{}
			repo.On("LoadSpec", mock.Anything, record.ID).Return(demoSpecBytes, nil).Once()
			codec := &mockCodec{}
			codec.On("Parse", mock.Anything, demoSpecBytes).Return(tt.previous, tt.parseErr).Once()
			svc := New(Deps{SessionLocker: fakeSessionLocker{}, SavepointRepo: repo, SpecCodec: codec})
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			svc.warnCapturePolicyDrift(ctx, []model.Savepoint{record},
				stampedProject(types.Labels{string(model.NewMountStampLabel("/cache")): excludedStamp}))

			// Assert
			assert.Contains(t, logs.String(), tt.want)
			assert.Contains(t, logs.String(), `"savepoint":"sp"`)
			repo.AssertExpectations(t)
			codec.AssertExpectations(t)
		})
	}
}
