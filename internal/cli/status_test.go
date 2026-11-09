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

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestStatusHandler_Status(t *testing.T) {
	req := model.StatusRequest{Session: "feat"}
	status := model.ComposeProjectStatus{ComposeProject: "demo", State: model.ComposeProjectStateRunning}
	daemonErr := errors.New("daemon unreachable")
	tests := []struct {
		name        string
		svcErr      error
		wantErr     error
		wantPrinted []any
	}{
		{name: "a full status prints and succeeds", wantPrinted: []any{status}},
		{
			name: "a partial status prints and still fails", svcErr: model.ErrStatusPartial,
			wantErr: model.ErrStatusPartial, wantPrinted: []any{status},
		},
		{name: "a daemon that cannot be read prints nothing", svcErr: daemonErr, wantErr: daemonErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			printer := &capturePrinter{}
			for _, printed := range tt.wantPrinted {
				printer.On("Print", printed).Return(nil).Once()
			}
			svc := &mockService{}
			svc.On("Status", mock.Anything, req).Return(status, tt.svcErr).Once()

			// Act
			err := NewStatusHandler(svc, printer).Status(context.Background(), req)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			svc.AssertExpectations(t)
			printer.AssertExpectations(t)
		})
	}
}
