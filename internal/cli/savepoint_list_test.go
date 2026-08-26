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

	"github.com/greenmaskio/arcave/internal/model"
)

// The list handler prints whatever the service returns, verbatim. A service
// error must short-circuit before the printer runs; a printer error must reach
// the caller.
func TestSavepointListHandler_SavepointList(t *testing.T) {
	sps := []model.Savepoint{
		{ID: "1780335122", Name: "before-migration"},
		{ID: "1780335999", Name: "after-migration"},
	}
	svcErr := errors.New("manifest unreadable")
	printErr := errors.New("stdout closed")

	tests := []struct {
		name        string
		req         model.ListRequest // what the command asked for
		savepoints  []model.Savepoint // what the service returns
		listErr     error             // the service's error, if any
		printerErr  error             // the printer's error, if any
		wantErr     error             // what SavepointList must return
		wantPrinted []any             // every value handed to the printer, in order
	}{
		{
			name:        "prints every savepoint",
			savepoints:  sps,
			listErr:     nil,
			printerErr:  nil,
			wantErr:     nil,
			wantPrinted: []any{sps},
		},
		{
			// The flag is the handler's only input, and it must reach the service
			// verbatim: matched exactly below, so dropping it fails here.
			name:        "--include-safety-snapshots reaches the service",
			req:         model.ListRequest{IncludeSafetySnapshots: true},
			savepoints:  sps,
			wantPrinted: []any{sps},
		},
		{
			name:        "empty manifest prints an empty list",
			savepoints:  []model.Savepoint{},
			listErr:     nil,
			printerErr:  nil,
			wantErr:     nil,
			wantPrinted: []any{[]model.Savepoint{}},
		},
		{
			name:        "service error skips printing",
			savepoints:  nil,
			listErr:     svcErr,
			printerErr:  nil,
			wantErr:     svcErr,
			wantPrinted: nil,
		},
		{
			name:        "printer error surfaces",
			savepoints:  sps,
			listErr:     nil,
			printerErr:  printErr,
			wantErr:     printErr,
			wantPrinted: []any{sps},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			p := &capturePrinter{}
			p.On("Print", mock.Anything).Return(tt.printerErr).Maybe()
			svc := &mockService{}
			svc.On("ListSavepoints", mock.Anything, tt.req).Return(tt.savepoints, tt.listErr).Once()
			h := NewSavepointListHandler(svc, p)

			// Act
			err := h.SavepointList(context.Background(), tt.req)

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantPrinted, p.printed())
			svc.AssertExpectations(t)
		})
	}
}
