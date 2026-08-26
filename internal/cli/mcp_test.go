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

func TestMCPHandler_listSavepointsMCP(t *testing.T) {
	sps := []model.Savepoint{{ID: "1780335122", Name: "before-migration"}}

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
			h := NewMCPHandler(factory)

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
	})

	// Act
	_, out, err := h.listSavepointsMCP(context.Background(), nil, listSavepointInput{Project: "nope"})

	// Assert
	require.ErrorIs(t, err, wireErr)
	assert.Equal(t, listOutput{}, out)
	svc.AssertExpectations(t)
}
