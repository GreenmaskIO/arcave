package cli

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

func TestSavepointRemoveHandler_Remove(t *testing.T) {
	retainRecent := 3
	retainFor := 7 * 24 * time.Hour
	beforeDate := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	base := apimodel.SavepointRemoveRequest{
		ProjectName: "demo",
		SessionID:   testSessionID,
		ProviderID:  "my-linux-box",
		StackID:     "web",
	}
	req := func(mut func(r *apimodel.SavepointRemoveRequest)) *apimodel.SavepointRemoveRequest {
		r := base
		mut(&r)
		return &r
	}

	tests := []struct {
		name       string
		addressing Addressing
		env        string
		params     SavepointRemoveParams
		wantReq    *apimodel.SavepointRemoveRequest
		apiErr     error
		wantErr    string
	}{
		{
			name:       "explicit ids",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointRemoveParams{IDs: []string{testSavepointID.String()}},
			wantReq: req(func(r *apimodel.SavepointRemoveRequest) {
				r.SavepointIDs = []uuid.UUID{testSavepointID}
			}),
		},
		{
			name:       "every retention rule, parsed",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params: SavepointRemoveParams{
				RetainRecent: &retainRecent,
				RetainFor:    "1w",
				BeforeDate:   "2026-01-02T03:04:05Z",
				DryRun:       true,
			},
			wantReq: req(func(r *apimodel.SavepointRemoveRequest) {
				r.RetainRecent, r.RetainFor, r.BeforeDate, r.DryRun = &retainRecent, &retainFor, &beforeDate, true
			}),
		},
		{
			name:       "a retain-recent of zero is a count, not an absence",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointRemoveParams{RetainRecent: new(int)},
			wantReq:    req(func(r *apimodel.SavepointRemoveRequest) { r.RetainRecent = new(int) }),
		},
		{
			name:       "an id that is not a uuid is refused before addressing",
			addressing: Addressing{},
			params:     SavepointRemoveParams{IDs: []string{"latest"}},
			wantErr:    `parse savepoint id "latest"`,
		},
		{
			name:       "a retain-for without a unit is refused",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointRemoveParams{RetainFor: "seven"},
			wantErr:    "parse --retain-for",
		},
		{
			name:       "a before-date that is not a timestamp is refused",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointRemoveParams{BeforeDate: "2026-01-02"},
			wantErr:    "parse --before-date",
		},
		{
			name:       "an unnamed session reaches nothing",
			addressing: Addressing{Stack: "web"},
			params:     SavepointRemoveParams{IDs: []string{testSavepointID.String()}},
			wantErr:    "arcave session create",
		},
		{
			name:       "api error passes through",
			addressing: Addressing{Stack: "web"},
			env:        testSessionID.String(),
			params:     SavepointRemoveParams{IDs: []string{testSavepointID.String()}},
			wantReq: req(func(r *apimodel.SavepointRemoveRequest) {
				r.SavepointIDs = []uuid.UUID{testSavepointID}
			}),
			apiErr:  errAPI,
			wantErr: errAPI.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			t.Setenv(EnvSessionID, tt.env)
			api := &mockAPI{}
			if tt.wantReq != nil {
				api.On("SavepointRemove", mock.Anything, *tt.wantReq).Return(tt.apiErr)
			}
			h := NewSavepointRemoveHandler(api)

			// Act
			err := h.Remove(context.Background(), testConfig, tt.addressing, tt.params)

			// Assert
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			api.AssertExpectations(t)
		})
	}
}

func TestSavepointRemoveHandler_Remove_selectorSentinels(t *testing.T) {
	t.Setenv(EnvSessionID, testSessionID.String())
	retainRecent := 3

	tests := []struct {
		name    string
		params  SavepointRemoveParams
		wantErr error
	}{
		{
			name:    "ids combined with a retention rule",
			params:  SavepointRemoveParams{IDs: []string{testSavepointID.String()}, RetainRecent: &retainRecent},
			wantErr: apimodel.ErrRemoveSelectorConflict,
		},
		{
			name:    "no selector at all",
			params:  SavepointRemoveParams{},
			wantErr: apimodel.ErrRemoveNoSelector,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			h := NewSavepointRemoveHandler(api)

			// Act
			err := h.Remove(context.Background(), testConfig, Addressing{Stack: "web"}, tt.params)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			api.AssertExpectations(t)
		})
	}
}

func TestSavepointRemoveHandler_Remove_negativeRetainRecent(t *testing.T) {
	// Arrange
	t.Setenv(EnvSessionID, testSessionID.String())
	negative := -1
	api := &mockAPI{}
	h := NewSavepointRemoveHandler(api)

	// Act
	err := h.Remove(context.Background(), testConfig, Addressing{Stack: "web"},
		SavepointRemoveParams{RetainRecent: &negative})

	// Assert
	require.ErrorIs(t, err, apimodel.ErrRetainRecentNegative)
	api.AssertExpectations(t)
}
