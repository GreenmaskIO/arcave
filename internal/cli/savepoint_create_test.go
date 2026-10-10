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

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

func TestSavepointCreateHandler_SavepointCreate(t *testing.T) {
	cfg := config.Config{Compose: config.Compose{WaitTimeout: waitTimeout}}
	wantReq := model.CreateSavepointRequest{
		Name:        "before-migration",
		Description: "schema v2",
		WaitTimeout: waitTimeout,
	}
	sp := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "before-migration", Description: "schema v2",
		NotCovered: &model.NotCovered{ServicesWithoutVolumes: []string{"web"}}}
	svcErr := errors.New("services did not become healthy")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name        string
		sp          model.Savepoint // what the service returns
		svcErr      error           // the service's error, if any
		printerErr  error           // the printer's error, if any
		wantErr     error           // what SavepointCreate must return
		wantPrinted []any           // every value handed to the printer, in order
	}{
		{
			name:        "prints the created savepoint",
			sp:          sp,
			svcErr:      nil,
			printerErr:  nil,
			wantErr:     nil,
			wantPrinted: []any{sp},
		},
		{
			name:        "service error skips printing",
			sp:          model.Savepoint{},
			svcErr:      svcErr,
			printerErr:  nil,
			wantErr:     svcErr,
			wantPrinted: nil,
		},
		{
			name:        "printer error surfaces",
			sp:          sp,
			svcErr:      nil,
			printerErr:  printErr,
			wantErr:     printErr,
			wantPrinted: []any{sp},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(tt.printerErr).Maybe()
			svc := &mockService{}
			svc.On("CreateSavepoint", mock.Anything, wantReq).Return(tt.sp, tt.svcErr)
			h := NewSavepointCreateHandler(svc, p)

			// Act
			err := h.SavepointCreate(context.Background(), cfg, "before-migration", "schema v2")

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
			p.AssertExpectations(t)
		})
	}
}
