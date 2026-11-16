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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestDownHandler_Down(t *testing.T) {
	// Arrange
	res := model.DownResult{ComposeProject: "demo", Forced: true, RemovedTags: []model.ImageRef{}}
	svc := &mockService{}
	svc.On("Down", mock.Anything, model.DownRequest{Force: true}).Return(res, nil).Once()
	output := &capturePrinter{}
	output.On("Print", res).Return(nil).Once()

	// Act
	err := NewDownHandler(svc, output).Down(context.Background(), true)

	// Assert
	require.NoError(t, err)
	svc.AssertExpectations(t)
	output.AssertExpectations(t)
}

// Each refusal keeps its sentinel and gains the fix in the spelling of the surface it reaches.
func TestDeploymentError(t *testing.T) {
	elsewhere := &model.DeployedElsewhereError{
		ArcaveProject: "shop",
		Deployment:    model.SavepointDeployment{ArcaveProject: "shop", Session: model.SessionRef{Name: "feat"}},
	}
	tests := []struct {
		name     string
		err      error
		hints    deploymentFixHints
		wantIs   error
		wantHint string
	}{
		{
			name: "up over a present compose project, cli", err: &model.ComposeProjectPresentError{Volumes: []string{"demo_db"}},
			hints: cliDeploymentFixHints, wantIs: model.ErrComposeProjectPresent, wantHint: "pass --force to drop all of it first",
		},
		{
			name: "up over a present compose project, mcp", err: &model.ComposeProjectPresentError{Volumes: []string{"demo_db"}},
			hints: mcpDeploymentFixHints, wantIs: model.ErrComposeProjectPresent, wantHint: "the rollback tool replaces",
		},
		{
			name: "down of another session's, cli", err: elsewhere, hints: cliDeploymentFixHints,
			wantIs: model.ErrDeployedElsewhere, wantHint: "--session feat",
		},
		{
			name: "down of another session's, mcp", err: elsewhere, hints: mcpDeploymentFixHints,
			wantIs: model.ErrDeployedElsewhere, wantHint: "call it with session feat",
		},
		{
			name: "down of compose's own", err: &model.NotDeployedByArcaveError{Replicas: []string{"db-1"}}, hints: cliDeploymentFixHints,
			wantIs: model.ErrNotDeployedByArcave, wantHint: "pass --force to take it down anyway",
		},
		{
			name: "no savepoint", err: model.ErrNoSavepoints, hints: cliDeploymentFixHints,
			wantIs: model.ErrNoSavepoints, wantHint: "arcave savepoint create",
		},
		{
			name: "a drop held back", err: &model.ComposeProjectLeftError{Volumes: []string{"demo_data"}}, hints: cliDeploymentFixHints,
			wantIs: model.ErrComposeProjectLeft, wantHint: "stop or remove what holds them",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := deploymentError(tt.err, tt.hints)

			// Assert
			require.ErrorIs(t, err, tt.wantIs)
			assert.Contains(t, err.Error(), tt.wantHint)
		})
	}
}
