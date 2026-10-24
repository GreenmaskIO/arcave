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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestGCHandler_GarbageCollect(t *testing.T) {
	req := model.GarbageCollectRequest{DryRun: true, DataKinds: []model.GarbageDataKind{model.GarbageDataKindUpstreamImages}}
	res := model.GarbageCollectResult{DryRun: true, Session: model.DefaultSession}
	svcErr := errors.New("store unreachable")

	tests := []struct {
		name        string
		svcErr      error
		wantErr     error
		wantPrinted []any
	}{
		{name: "prints the report", wantPrinted: []any{res}},
		{
			name: "a partial gc prints the report and still fails", svcErr: model.ErrGarbageCollectPartial,
			wantErr: model.ErrGarbageCollectPartial, wantPrinted: []any{res},
		},
		{name: "any other error prints nothing", svcErr: svcErr, wantErr: svcErr},
		{
			name: "a gc that would delete says how to run one", svcErr: model.ErrGarbageCollectNotImplemented,
			wantErr: model.ErrGarbageCollectNotImplemented,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			printer := &capturePrinter{}
			printer.On("Print", mock.Anything).Return(nil).Maybe()
			svc := &mockService{}
			svc.On("GarbageCollect", mock.Anything, req).Return(res, tt.svcErr).Once()

			// Act
			err := NewGCHandler(svc, printer).GarbageCollect(context.Background(), req)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPrinted, printer.printed())
			svc.AssertExpectations(t)
		})
	}
}

// The gc tool's output schema must accept every kind's detail: it is an interface, and the SDK
// validates each result against the schema inferred for it.
func TestGCOutputSchema_AcceptsADetail(t *testing.T) {
	// Arrange
	resolved, err := newOutputSchema[model.GarbageCollectResult]().Resolve(nil)
	require.NoError(t, err)
	size := int64(3)
	result := model.GarbageCollectResult{DryRun: true, Session: model.DefaultSession, Reports: []model.GarbageReport{{
		DataKind: model.GarbageDataKindImageLayers, ComposeProject: "demo", Summary: model.GarbageSummary{Found: 1, Bytes: &size},
		Detail: model.OCIBlobStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{{
			Action: model.GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/ab", Bytes: &size,
			Reason: model.GarbageReasonUnreferenced, LastModified: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		}}}},
	}}}
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var instance map[string]any
	require.NoError(t, json.Unmarshal(data, &instance))

	// Act
	err = resolved.Validate(instance)

	// Assert
	require.NoError(t, err)
}

// A request the service would refuse is refused before anything is opened.
func TestValidateGarbageCollectRequest(t *testing.T) {
	negative := -time.Hour

	tests := []struct {
		name    string
		req     model.GarbageCollectRequest
		wantErr error
		wantMsg string
	}{
		{name: "a dry run", req: model.GarbageCollectRequest{DryRun: true}},
		{
			name: "one that would delete says how to run one", req: model.GarbageCollectRequest{},
			wantErr: model.ErrGarbageCollectNotImplemented, wantMsg: "pass --dry-run",
		},
		{
			name:    "an unknown kind",
			req:     model.GarbageCollectRequest{DryRun: true, DataKinds: []model.GarbageDataKind{"restic"}},
			wantErr: model.ErrInvalidGarbageCollectRequest,
		},
		{
			name:    "a negative grace",
			req:     model.GarbageCollectRequest{DryRun: true, RestoredLayerGrace: &negative},
			wantErr: model.ErrInvalidGarbageCollectRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := ValidateGarbageCollectRequest(tt.req)

			// Assert
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}
