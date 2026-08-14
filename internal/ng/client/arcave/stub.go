// Package arcave is the client the CLI reaches the control plane through.
package arcave

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/greenmaskio/arcave/internal/model"
	apimodel "github.com/greenmaskio/arcave/internal/model/api"
)

// Stub stands in for the control plane client with one canned response per method.
type Stub struct{}

// NewStub builds the stub client.
func NewStub() *Stub { return &Stub{} }

func (s *Stub) ProjectApply(_ context.Context, req apimodel.ProjectApplyRequest) (apimodel.ProjectApplyResponse, error) {
	return apimodel.ProjectApplyResponse{
		Name:   req.Project.Name,
		Status: apimodel.ProjectApplyStatusCreated,
	}, nil
}

func (s *Stub) ProjectList(context.Context) (apimodel.ProjectListResponse, error) {
	return apimodel.ProjectListResponse{
		Projects: []apimodel.ProjectListItem{
			{Name: "myapp", CreatedAt: stubCreatedAt, Sessions: 1},
		},
	}, nil
}

func (s *Stub) ProjectRemove(context.Context, apimodel.ProjectRemoveRequest) error {
	return nil
}

func (s *Stub) SessionCreate(_ context.Context, req apimodel.SessionCreateRequest) (apimodel.SessionCreateResponse, error) {
	return apimodel.SessionCreateResponse{
		ProjectName: req.ProjectName,
		SessionID:   uuid.New(),
		CreatedAt:   time.Now().UTC(),
		From:        req.FromSessionID,
	}, nil
}

func (s *Stub) SessionList(_ context.Context, req apimodel.SessionListRequest) (apimodel.SessionListResponse, error) {
	return apimodel.SessionListResponse{
		ProjectName: req.ProjectName,
		Sessions: []apimodel.SessionListItem{
			{SessionID: stubSessionID, CreatedAt: stubCreatedAt},
		},
	}, nil
}

func (s *Stub) SessionStatus(_ context.Context, req apimodel.SessionStatusRequest) (any, error) {
	return map[string]any{
		"project_name": req.ProjectName,
		"session_id":   req.SessionID,
		"status":       "active",
	}, nil
}

func (s *Stub) SavepointCreate(_ context.Context, req apimodel.SavepointCreateRequest) (apimodel.SavepointCreateResponse, error) {
	return apimodel.SavepointCreateResponse{
		ProviderID:   req.ProviderID,
		StackName:    req.StackID,
		SavepointID:  uuid.New(),
		ProviderType: stubProviderType,
	}, nil
}

func (s *Stub) SavepointList(_ context.Context, req apimodel.SavepointListRequest) (apimodel.SavepointListResponse, error) {
	return apimodel.SavepointListResponse{
		SessionID:    stubSessionID,
		ProviderID:   req.ProviderID,
		StackName:    req.StackID,
		ProviderType: stubProviderType,
		CurrentPage:  1,
		TotalPages:   1,
		PageSize:     req.Limit,
		Records: []apimodel.SavepointRecord{
			{SavepointID: stubSavepointID, CreatedAt: stubCreatedAt, Name: "demo"},
		},
	}, nil
}

func (s *Stub) SavepointRemove(context.Context, apimodel.SavepointRemoveRequest) error {
	return nil
}

func (s *Stub) SavepointRollback(_ context.Context, req apimodel.SavepointRollbackRequest) (apimodel.SavepointRollbackResponse, error) {
	return apimodel.SavepointRollbackResponse{
		StartedAt:                  stubCreatedAt,
		CompletedAt:                stubCreatedAt,
		RollbackItemStatues:        []apimodel.ItemStatus{},
		SafetySavepointItemStatues: []apimodel.ItemStatus{},
		SafetySavepointID:          uuid.New(),
		Status:                     apimodel.SavepointRollbackStatusSuccess,
	}, nil
}

func (s *Stub) Recommend(_ context.Context, req apimodel.RecommendRequest) (apimodel.RecommendResponse, error) {
	return apimodel.RecommendResponse{
		ProviderID: req.ProviderID,
		StackName:  req.StackID,
		// Empty, not a canned finding: `recommend` exits 1 when it has advice.
		Recommendations: []model.Recommendation{},
	}, nil
}

func (s *Stub) ProviderList(context.Context) (apimodel.ProviderListResponse, error) {
	return apimodel.ProviderListResponse{
		Providers: []apimodel.ProviderListItem{
			{ID: "my-linux-box", Name: "my-linux-box", Type: "linux", Status: "ready"},
		},
	}, nil
}

// The fixed values the canned responses are built from.
var (
	stubSessionID   = uuid.MustParse("6f8f2f0e-1a5c-4a2e-9d4b-0f1f2a3b4c5d")
	stubSavepointID = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	stubCreatedAt   = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
)

const stubProviderType = "linux"
