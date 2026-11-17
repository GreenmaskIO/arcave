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

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

// The ref reads as a rollback's, and none is the newest savepoint; the wait bound is the config's.
func TestUpHandler_Up(t *testing.T) {
	id := model.SavepointID("019e843e-1650-7000-8000-000000000000")
	tests := []struct {
		name    string
		ref     string
		force   bool
		wantReq model.UpRequest
	}{
		{name: "no ref is latest", wantReq: model.UpRequest{Latest: ptr(true), WaitTimeout: waitTimeout}},
		{name: "a name", ref: "base", wantReq: model.UpRequest{Name: ptr("base"), WaitTimeout: waitTimeout}},
		{name: "an id, forced", ref: id.String(), force: true, wantReq: model.UpRequest{ID: ptr(id), WaitTimeout: waitTimeout, Force: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			res := model.UpResult{RolledBackTo: model.Savepoint{ID: id}, Restored: []model.Volume{}}
			svc := &mockService{}
			svc.On("Up", mock.Anything, tt.wantReq).Return(res, nil).Once()
			output := &capturePrinter{}
			output.On("Print", res).Return(nil).Once()
			cfg := config.Config{Compose: config.Compose{WaitTimeout: waitTimeout}}

			// Act
			err := NewUpHandler(svc, output).Up(context.Background(), cfg, tt.ref, tt.force)

			// Assert
			require.NoError(t, err)
			svc.AssertExpectations(t)
			output.AssertExpectations(t)
		})
	}
}
