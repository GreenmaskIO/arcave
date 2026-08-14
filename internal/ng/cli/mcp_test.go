package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// mcpConfig and mcpAddressing resolve to the addressing the server is bound to.
var (
	mcpConfig = config.Config{Project: config.Project{
		Name:      "myapp",
		Providers: config.Providers{Stacks: []string{"compose"}},
	}}
	mcpAddressing = Addressing{SessionID: testSessionID.String(), Stack: "demo"}
)

// newTestMCPHandler builds the handler every tool test calls.
func newTestMCPHandler(t *testing.T, api mcpAPI) *MCPHandler {
	t.Helper()

	h, err := NewMCPHandler(api, mcpConfig, mcpAddressing)
	require.NoError(t, err)
	return h
}

func TestMCPHandler_savepointCreate(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("SavepointCreate", mock.Anything, apimodel.SavepointCreateRequest{
		ProjectName: "myapp",
		SessionID:   testSessionID,
		ProviderID:  "compose",
		StackID:     "demo",
		Name:        "before-migration",
		Labels:      map[string]string{"kind": "manual"},
	}).Return(apimodel.SavepointCreateResponse{
		ProviderID:   "compose",
		StackName:    "demo",
		SavepointID:  testSavepointID,
		ProviderType: "linux",
	}, nil)
	h := newTestMCPHandler(t, api)

	// Act
	_, out, err := h.savepointCreate(context.Background(), nil, createInput{
		Name:   "before-migration",
		Labels: map[string]string{"kind": "manual"},
	})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, createOutput{
		SavepointID:  testSavepointID.String(),
		ProviderID:   "compose",
		Stack:        "demo",
		ProviderType: "linux",
	}, out)
	api.AssertExpectations(t)
}

func TestMCPHandler_savepointList(t *testing.T) {
	// Arrange
	createdAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	api := &mockAPI{}
	api.On("SavepointList", mock.Anything, apimodel.SavepointListRequest{
		ProjectName: "myapp",
		ProviderID:  "compose",
		StackID:     "demo",
		Limit:       5,
	}).Return(apimodel.SavepointListResponse{
		ProviderID:  "compose",
		StackName:   "demo",
		CurrentPage: 1,
		TotalPages:  1,
		PageSize:    5,
		Records: []apimodel.SavepointRecord{
			{SavepointID: testSavepointID, CreatedAt: createdAt, Name: "demo"},
		},
	}, nil)
	h := newTestMCPHandler(t, api)

	// Act
	_, out, err := h.savepointList(context.Background(), nil, listInput{Limit: 5})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []savepointOut{{
		SavepointID: testSavepointID.String(),
		CreatedAt:   "2026-08-13T12:00:00Z",
		Name:        "demo",
	}}, out.Savepoints)
	assert.Equal(t, "demo", out.Stack)
	api.AssertExpectations(t)
}

// An empty history answers with [] rather than null.
func TestMCPHandler_savepointList_Empty(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("SavepointList", mock.Anything, mock.Anything).
		Return(apimodel.SavepointListResponse{StackName: "demo"}, nil)
	h := newTestMCPHandler(t, api)

	// Act
	_, out, err := h.savepointList(context.Background(), nil, listInput{})

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, out.Savepoints)
	assert.Empty(t, out.Savepoints)
	api.AssertExpectations(t)
}

func TestMCPHandler_removeRequest(t *testing.T) {
	retainRecent := 3

	tests := []struct {
		name       string
		in         removeInput
		wantErrMsg string
		verify     func(t *testing.T, req apimodel.SavepointRemoveRequest)
	}{
		{
			name: "no selector leaves every pointer nil",
			in:   removeInput{},
			verify: func(t *testing.T, req apimodel.SavepointRemoveRequest) {
				assert.Empty(t, req.SavepointIDs)
				assert.Nil(t, req.RetainRecent)
				assert.Nil(t, req.RetainFor)
				assert.Nil(t, req.BeforeDate)
			},
		},
		{
			name: "ids are parsed",
			in:   removeInput{IDs: []string{testSavepointID.String()}},
			verify: func(t *testing.T, req apimodel.SavepointRemoveRequest) {
				assert.Equal(t, []uuid.UUID{testSavepointID}, req.SavepointIDs)
			},
		},
		{
			name: "a zero retain_recent is still a selector",
			in:   removeInput{RetainRecent: new(int)},
			verify: func(t *testing.T, req apimodel.SavepointRemoveRequest) {
				require.NotNil(t, req.RetainRecent)
				assert.Equal(t, 0, *req.RetainRecent)
			},
		},
		{
			name: "retain_for takes the extended duration units",
			in:   removeInput{RetainRecent: &retainRecent, RetainFor: "1w2d"},
			verify: func(t *testing.T, req apimodel.SavepointRemoveRequest) {
				require.NotNil(t, req.RetainFor)
				assert.Equal(t, 9*24*time.Hour, *req.RetainFor)
			},
		},
		{
			name: "before_date takes a plain date",
			in:   removeInput{BeforeDate: "2026-08-01"},
			verify: func(t *testing.T, req apimodel.SavepointRemoveRequest) {
				require.NotNil(t, req.BeforeDate)
				assert.True(t, req.BeforeDate.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)),
					"before_date = %v", req.BeforeDate)
			},
		},
		{
			name: "before_date takes a full timestamp",
			in:   removeInput{BeforeDate: "2026-08-01T10:30:00Z"},
			verify: func(t *testing.T, req apimodel.SavepointRemoveRequest) {
				require.NotNil(t, req.BeforeDate)
				assert.True(t, req.BeforeDate.Equal(time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)),
					"before_date = %v", req.BeforeDate)
			},
		},
		{name: "an id that is not a uuid", in: removeInput{IDs: []string{"nope"}}, wantErrMsg: "want a uuid"},
		{name: "a duration with no unit", in: removeInput{RetainFor: "7"}, wantErrMsg: "retain_for"},
		{name: "a date in no known form", in: removeInput{BeforeDate: "last monday"}, wantErrMsg: "before_date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			h := newTestMCPHandler(t, &mockAPI{})

			// Act
			req, err := h.removeRequest(tt.in)

			// Assert
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "myapp", req.ProjectName)
			assert.Equal(t, testSessionID, req.SessionID)
			assert.Equal(t, "demo", req.StackID)
			tt.verify(t, req)
		})
	}
}

func TestMCPHandler_savepointRemove_NoSelector(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	h := newTestMCPHandler(t, api)

	// Act
	_, _, err := h.savepointRemove(context.Background(), nil, removeInput{DryRun: true})

	// Assert
	require.ErrorIs(t, err, apimodel.ErrRemoveNoSelector)
	assert.ErrorContains(t, err, "retain_recent")
	api.AssertExpectations(t)
}

func TestMCPHandler_rollback(t *testing.T) {
	itemErr := error(errors.New("volume is busy"))
	startedAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	// Arrange
	api := &mockAPI{}
	api.On("SavepointRollback", mock.Anything, apimodel.SavepointRollbackRequest{
		ProjectName:         "myapp",
		SessionID:           testSessionID,
		ProviderID:          "compose",
		StackID:             "demo",
		SavepointID:         testSavepointID,
		WithSafetySavepoint: true,
	}).Return(apimodel.SavepointRollbackResponse{
		StartedAt:         startedAt,
		CompletedAt:       startedAt.Add(time.Minute),
		SafetySavepointID: testSessionID,
		RollbackItemStatues: []apimodel.ItemStatus{{
			Identity: model.ObjectIdentity{
				Kind:       "volume",
				NameParts:  []string{"stack", "volume"},
				NameValues: map[string]string{"stack": "demo", "volume": "pgdata"},
			},
			Status: "failed",
			Error:  &itemErr,
		}},
		Status: apimodel.SavepointRollbackStatusWarning,
	}, nil)
	h := newTestMCPHandler(t, api)

	// Act
	_, out, err := h.rollback(context.Background(), nil, rollbackInput{SavepointID: testSavepointID.String()})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "warning", out.Status)
	assert.Equal(t, "2026-08-13T12:00:00Z", out.StartedAt)
	assert.Equal(t, testSessionID.String(), out.SafetySavepointID)
	assert.Equal(t, []itemOut{{
		Name:   "demo.pgdata",
		Kind:   "volume",
		Status: "failed",
		Error:  "volume is busy",
	}}, out.RolledBack)
	assert.NotNil(t, out.SafetySavepoint)
	api.AssertExpectations(t)
}

func TestMCPHandler_rollback_SafetyDefaultsOn(t *testing.T) {
	off := false

	tests := []struct {
		name string
		in   *bool
		want bool
	}{
		{name: "omitted", want: true},
		{name: "explicitly off", in: &off, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			api := &mockAPI{}
			api.On("SavepointRollback", mock.Anything, mock.MatchedBy(
				func(req apimodel.SavepointRollbackRequest) bool { return req.WithSafetySavepoint == tt.want },
			)).Return(apimodel.SavepointRollbackResponse{Status: apimodel.SavepointRollbackStatusSuccess}, nil)
			h := newTestMCPHandler(t, api)

			// Act
			_, _, err := h.rollback(context.Background(), nil, rollbackInput{
				SavepointID:         testSavepointID.String(),
				WithSafetySavepoint: tt.in,
			})

			// Assert
			require.NoError(t, err)
			api.AssertExpectations(t)
		})
	}
}

func TestMCPHandler_rollback_BadID(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	h := newTestMCPHandler(t, api)

	// Act
	_, _, err := h.rollback(context.Background(), nil, rollbackInput{SavepointID: "nope"})

	// Assert
	require.ErrorContains(t, err, "want a uuid")
	api.AssertExpectations(t)
}

func TestMCPHandler_recommend(t *testing.T) {
	// Arrange
	api := &mockAPI{}
	api.On("Recommend", mock.Anything, apimodel.RecommendRequest{
		ProjectName: "myapp",
		ProviderID:  "compose",
		StackID:     "demo",
	}).Return(apimodel.RecommendResponse{StackName: "demo"}, nil)
	h := newTestMCPHandler(t, api)

	// Act
	_, out, err := h.recommend(context.Background(), nil, recommendInput{})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "demo", out.Stack)
	assert.NotNil(t, out.Recommendations, "an empty finding list marshals as [], never null")
	api.AssertExpectations(t)
}

func TestNewMCPHandler_unresolvableScope(t *testing.T) {
	// Arrange
	api := &mockAPI{}

	// Act
	h, err := NewMCPHandler(api, mcpConfig, Addressing{SessionID: testSessionID.String()})

	// Assert
	require.ErrorContains(t, err, "no stack")
	assert.Nil(t, h)
	api.AssertExpectations(t)
}
