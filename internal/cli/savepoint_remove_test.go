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

// The service's selector-validation sentinels carry no flag names; only this
// layer knows them, so it rephrases each one in terms of the flags the user
// typed. Any other error passes through untouched. Because the rephrased errors
// are new values rather than wrapped sentinels, the expectation is the message,
// not errors.Is.
func TestSavepointRemoveHandler_SavepointRemove(t *testing.T) {
	req := model.RemoveRequest{DryRun: true, IDs: []model.SavepointID{"019e843e-1650-7000-8000-000000000000"}}
	res := model.RemoveResult{DryRun: true, Removed: []model.Savepoint{{ID: "019e843e-1650-7000-8000-000000000000"}}}
	other := errors.New("disk on fire")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name        string
		res         model.RemoveResult // what the service returns
		svcErr      error              // the service's error, if any
		printerErr  error              // the printer's error, if any
		wantErrMsg  string             // SavepointRemove's error message; empty means no error
		wantPrinted []any              // every value handed to the printer, in order
	}{
		{
			name:        "prints what was removed",
			res:         res,
			svcErr:      nil,
			printerErr:  nil,
			wantErrMsg:  "",
			wantPrinted: []any{model.RemoveResponse(res)},
		},
		{
			name:        "selector conflict",
			res:         model.RemoveResult{},
			svcErr:      model.ErrRemoveSelectorConflict,
			printerErr:  nil,
			wantErrMsg:  "cannot combine savepoint ids with retain-recent/retain-for/before-date",
			wantPrinted: nil,
		},
		{
			name:        "no selector",
			res:         model.RemoveResult{},
			svcErr:      model.ErrRemoveNoSelector,
			printerErr:  nil,
			wantErrMsg:  "specify savepoint ids, or at least one of retain-recent, retain-for or before-date",
			wantPrinted: nil,
		},
		{
			name:        "negative retain-recent",
			res:         model.RemoveResult{},
			svcErr:      model.ErrRetainRecentNegative,
			printerErr:  nil,
			wantErrMsg:  "retain-recent must be zero or positive",
			wantPrinted: nil,
		},
		{
			name:        "unrelated error passes through",
			res:         model.RemoveResult{},
			svcErr:      other,
			printerErr:  nil,
			wantErrMsg:  other.Error(),
			wantPrinted: nil,
		},
		{
			name:        "printer error surfaces",
			res:         res,
			svcErr:      nil,
			printerErr:  printErr,
			wantErrMsg:  printErr.Error(),
			wantPrinted: []any{model.RemoveResponse(res)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(tt.printerErr).Maybe()
			svc := &mockService{}
			svc.On("RemoveSavepoints", mock.Anything, req).Return(tt.res, tt.svcErr)
			h := NewSavepointRemoveHandler(svc, p)

			// Act
			err := h.SavepointRemove(context.Background(), req)

			// Assert
			if tt.wantErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Equal(t, tt.wantErrMsg, err.Error())
			}
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}
