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

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestMCPHandler_createSavepointMCP(t *testing.T) {
	// Arrange
	sp := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "before-migration", NotCovered: &model.NotCovered{
		Mounts: []model.SkippedVolume{{Service: "app", Target: "/scratch", RawType: "tmpfs",
			Reason: model.SkipReasonTmpfs, Detail: "non-persistent (tmpfs lives in RAM)"}},
		ServicesWithoutVolumes: []string{"web"},
		KilledContainers:       []model.KilledContainer{{Service: "db", Replica: 1, Container: "demo-db-1"}},
	}}
	svc := &mockService{}
	svc.On("CreateSavepoint", mock.Anything, model.CreateSavepointRequest{Name: "before-migration"}).
		Return(sp, nil).Once()
	released := false
	h := NewMCPHandler(func(context.Context, string) (MCPService, func(), error) {
		return svc, func() { released = true }, nil
	}, model.LeftoverVolumesOverwrite)

	// Act
	res, out, err := h.createSavepointMCP(context.Background(), nil, createInput{Project: "demo", Name: "before-migration"})

	// Assert
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.Equal(t, sp, out)
	assert.True(t, released, "what the call opened must not outlive it")
	svc.AssertExpectations(t)
}

func TestMCPHandler_listSavepointsMCP(t *testing.T) {
	sps := []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000", Name: "before-migration"}}

	tests := []struct {
		name string
		in   listSavepointInput
		svc  []model.Savepoint
		want listOutput
	}{
		{name: "hidden by default", in: listSavepointInput{Project: "demo"}, svc: sps, want: listOutput{Savepoints: sps}},
		{
			name: "include_safety_snapshots reaches the service",
			in:   listSavepointInput{Project: "demo", IncludeSafetySnapshots: true},
			svc:  sps,
			want: listOutput{Savepoints: sps},
		},
		{
			name: "no savepoints answers an empty array",
			in:   listSavepointInput{Project: "demo"},
			want: listOutput{Savepoints: []model.Savepoint{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := &mockService{}
			svc.On("ListSavepoints", mock.Anything,
				model.ListRequest{IncludeSafetySnapshots: tt.in.IncludeSafetySnapshots}).
				Return(tt.svc, nil).Once()
			var gotProject string
			released := false
			factory := func(_ context.Context, project string) (MCPService, func(), error) {
				gotProject = project
				return svc, func() { released = true }, nil
			}
			h := NewMCPHandler(factory, model.LeftoverVolumesOverwrite)

			// Act
			res, out, err := h.listSavepointsMCP(context.Background(), nil, tt.in)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, out)
			assert.Nil(t, res)
			assert.Equal(t, tt.in.Project, gotProject)
			assert.True(t, released, "what the call opened must not outlive it")
			svc.AssertExpectations(t)
		})
	}
}

func TestMCPHandler_listSavepointsMCP_AFactoryFailureSurfaces(t *testing.T) {
	// Arrange
	wireErr := errors.New("no such project")
	svc := &mockService{}
	h := NewMCPHandler(func(context.Context, string) (MCPService, func(), error) {
		return nil, nil, wireErr
	}, model.LeftoverVolumesOverwrite)

	// Act
	_, out, err := h.listSavepointsMCP(context.Background(), nil, listSavepointInput{Project: "nope"})

	// Assert
	require.ErrorIs(t, err, wireErr)
	assert.Equal(t, listOutput{}, out)
	svc.AssertExpectations(t)
}
