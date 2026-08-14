package api

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSavepointRemoveRequest_Validate(t *testing.T) {
	retainRecent, negativeRetainRecent := 3, -1
	retainFor := 7 * 24 * time.Hour
	beforeDate := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// scoped fills the whole addressing chain every accepted request needs.
	scoped := func(req SavepointRemoveRequest) SavepointRemoveRequest {
		req.ProjectName, req.SessionID, req.ProviderID, req.StackID = "myapp", testSessionID, "compose", "demo"
		return req
	}

	tests := []struct {
		name    string
		req     SavepointRemoveRequest
		wantErr error
	}{
		{name: "ids only", req: scoped(SavepointRemoveRequest{SavepointIDs: []uuid.UUID{testSavepointID}})},
		{name: "retain recent only", req: scoped(SavepointRemoveRequest{RetainRecent: &retainRecent})},
		{name: "retain for only", req: scoped(SavepointRemoveRequest{RetainFor: &retainFor})},
		{name: "before date only", req: scoped(SavepointRemoveRequest{BeforeDate: &beforeDate})},
		{name: "zero retain recent is a selector", req: scoped(SavepointRemoveRequest{RetainRecent: new(int)})},
		{
			name: "several retention rules",
			req:  scoped(SavepointRemoveRequest{RetainRecent: &retainRecent, BeforeDate: &beforeDate}),
		},
		{
			name: "ids combined with retention",
			req: scoped(SavepointRemoveRequest{
				SavepointIDs: []uuid.UUID{testSavepointID},
				RetainRecent: &retainRecent,
			}),
			wantErr: ErrRemoveSelectorConflict,
		},
		{
			name:    "no selector",
			req:     scoped(SavepointRemoveRequest{}),
			wantErr: ErrRemoveNoSelector,
		},
		{
			name:    "dry run is not a selector",
			req:     scoped(SavepointRemoveRequest{DryRun: true}),
			wantErr: ErrRemoveNoSelector,
		},
		{
			name:    "negative retain recent",
			req:     scoped(SavepointRemoveRequest{RetainRecent: &negativeRetainRecent}),
			wantErr: ErrRetainRecentNegative,
		},
		{
			name: "missing project name",
			req: SavepointRemoveRequest{
				SessionID:    testSessionID,
				ProviderID:   "compose",
				StackID:      "demo",
				RetainRecent: &retainRecent,
			},
			wantErr: ErrProjectNameRequired,
		},
		{
			name: "missing session id",
			req: SavepointRemoveRequest{
				ProjectName:  "myapp",
				ProviderID:   "compose",
				StackID:      "demo",
				RetainRecent: &retainRecent,
			},
			wantErr: ErrSessionIDRequired,
		},
		{
			name: "missing provider id",
			req: SavepointRemoveRequest{
				ProjectName:  "myapp",
				SessionID:    testSessionID,
				StackID:      "demo",
				RetainRecent: &retainRecent,
			},
			wantErr: ErrProviderIDRequired,
		},
		{
			name: "missing stack id",
			req: SavepointRemoveRequest{
				ProjectName:  "myapp",
				SessionID:    testSessionID,
				ProviderID:   "compose",
				RetainRecent: &retainRecent,
			},
			wantErr: ErrStackIDRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.req.Validate()

			// Assert
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}
