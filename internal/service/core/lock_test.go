package core

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// A workflow that cannot claim the project must abort before it touches
// anything: it must not stop the services a concurrent run is depending on, and
// it must not even read the savepoint store. The lock is taken first, so every
// collaborator below stays untouched — the mocks carry no expectations, and a
// stray call panics on the missing one.
func TestService_MutatingWorkflows_AbortWhenTheProjectIsLocked(t *testing.T) {
	tests := []struct {
		name string
		call func(svc *Service, ctx context.Context) error
	}{
		{
			name: "CreateSavepoint",
			call: func(svc *Service, ctx context.Context) error {
				_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})
				return err
			},
		},
		{
			name: "Rollback",
			call: func(svc *Service, ctx context.Context) error {
				_, err := svc.Rollback(ctx, model.RollbackRequest{Ref: "latest"})
				return err
			},
		},
		{
			name: "RemoveSavepoints",
			call: func(svc *Service, ctx context.Context) error {
				_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []model.SavepointID{"1"}})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			locker.On("Acquire", mock.Anything).Return(model.ErrProjectLocked)
			repo, inspector, runner := &mockSavepointRepo{}, &mockInspector{}, &mockComposeRunner{}
			registry := &mockRegistry{}
			// Nil stores: a workflow that got as far as a backend would deref one.
			svc := New(repo, repo, locker, inspector, runner, registry, nil, nil)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.call(svc, ctx)

			// Assert
			require.ErrorIs(t, err, model.ErrProjectLocked)
			inspector.AssertNotCalled(t, "Introspect", mock.Anything, mock.Anything)
			runner.AssertNotCalled(t, "Stop", mock.Anything, mock.Anything)
			runner.AssertNotCalled(t, "Up", mock.Anything, mock.Anything, mock.Anything)
			repo.AssertNotCalled(t, "List", mock.Anything)
			repo.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
			registry.AssertNotCalled(t, "New", mock.Anything)
			assert.Zero(t, locker.released, "a lock that was never taken must not be released")
			locker.AssertExpectations(t)
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}
