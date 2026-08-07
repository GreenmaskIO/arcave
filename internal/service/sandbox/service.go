package sandbox

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Service struct {
}

func New() *Service {
	return &Service{}
}

type SandboxSavepointIdentity struct {
	Session     uuid.UUID `json:"session"`
	SavepointID uuid.UUID `json:"savepoint_id"`
}

type StackSavepointIdentity struct {
	Name        string    `json:"stack_name"`
	Session     uuid.UUID `json:"session"`
	SavepointID uuid.UUID `json:"savepoint_id"`
}

type CreateSavepointRequest struct {
	Name                  string                   `json:"name"`
	Description           string                   `json:"description"`
	Labels                map[string]string        `json:"labels"`
	LinkedStackSavepoints []StackSavepointIdentity `json:"linked_stack_savepoints"`
}

type CreateSavepointResponse struct {
	Provider    string    `json:"provider"`
	SavepointID uuid.UUID `json:"savepoint_id"`
	Payload     any       `json:"payload"`
}

func (s *Service) CreateSavepoint(ctx context.Context, req CreateSavepointRequest) (CreateSavepointRequest, error) {
	panic("implement me")
}

type RollbackToSavepointRequest struct {
	FromSession         uuid.UUID `json:"from_session"`
	FromSavepoint       uuid.UUID `json:"from_savepoint"`
	WithSafetySavepoint bool      `json:"with_safety_savepoint"`
}

type Savepoint struct {
}

type RollbackToSavepointResponse struct {
	Provider              string     `json:"provider"`
	RestoredToSavepointID uuid.UUID  `json:"restored_to_savepoint_id"`
	Payload               any        `json:"payload"`
	HasSafetySavepoint    bool       `json:"has_safety_savepoint"`
	SafetySavepoint       *Savepoint `json:"safety_savepoint"`
}

func (s *Service) RollbackToSavepoint(
	ctx context.Context, req RollbackToSavepointRequest,
) (RollbackToSavepointResponse, error) {
	panic("implement me")
}

type ListSavepointsRequest struct {
	Limit                   int  `json:"limit"`
	Offset                  int  `json:"offset"`
	IncludeSafetySavepoints bool `json:"include_safety_savepoints"`
}

type ListSavepointsResponse struct {
	Provider    string      `json:"provider"`
	Savepoints  []Savepoint `json:"savepoints"`
	CurrentPage int         `json:"page"`
	TotalPages  int         `json:"total_pages"`
	Count       int         `json:"count"`
}

func (s *Service) ListSavepoints(
	ctx context.Context, req ListSavepointsRequest,
) (ListSavepointsResponse, error) {
	panic("implement me")
}

type RemoveSavepointsRequest struct {
	IDs          []uuid.UUID    `json:"ids"`
	RetainRecent *int           `json:"retain_recent"`
	RetainFor    *time.Duration `json:"retain_for"`
	BeforeDate   *time.Time     `json:"before_date"`
	DryRun       bool           `json:"dry_run"`
}

type SavepointWithError struct {
	SavepointID uuid.UUID         `json:"savepoint_id"`
	Err         error             `json:"error"`
	Meta        map[string]string `json:"meta"`
}

type RemoveSavepointsResponse struct {
	Provider             string               `json:"provider"`
	SavepointsRemoved    []uuid.UUID          `json:"savepoints_removed"`
	SavepointsWithErrors []SavepointWithError `json:"savepoints_with_errors"`
	DryRun               bool                 `json:"dry_run"`
}

func (s *Service) RemoveSavepoints(
	ctx context.Context, req RemoveSavepointsRequest,
) (RemoveSavepointsResponse, error) {
	panic("implement me")
}

func (s *Service) RunGarbageCollector(
	ctx context.Context,
) error {
	panic("implement me")
}
