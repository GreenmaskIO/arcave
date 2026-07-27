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
				_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []string{"1"}})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			locker := &mockLocker{}
			locker.On("Acquire").Return(model.ErrProjectLocked)
			repo, inspector, runner, backend := &mockSavepointRepo{}, &mockInspector{}, &mockComposeRunner{}, &mockBackend{}
			svc := New(repo, locker, inspector, runner, backend, backend)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.call(svc, ctx)

			// Assert
			require.ErrorIs(t, err, model.ErrProjectLocked)
			runner.AssertNotCalled(t, "Stop", mock.Anything, mock.Anything)
			runner.AssertNotCalled(t, "Up", mock.Anything, mock.Anything, mock.Anything)
			repo.AssertNotCalled(t, "List")
			repo.AssertNotCalled(t, "Delete", mock.Anything)
			backend.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything)
			assert.Zero(t, locker.released, "a lock that was never taken must not be released")
			locker.AssertExpectations(t)
		})
	}
}

// The lock is given back however the workflow ends, so a failed create does not
// wedge the project against the next command.
func TestService_CreateSavepoint_ReleasesTheLockOnFailure(t *testing.T) {
	// Arrange
	locker := freeLocker()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, mock.Anything).Return(model.IntrospectionResult{}, nil)
	svc := New(&mockSavepointRepo{}, locker, inspector, &mockComposeRunner{}, &mockBackend{}, nil)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act: no stateful mounts, so the workflow fails right after the lock is taken.
	_, err := svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.Error(t, err)
	assert.Equal(t, 1, locker.released)
}

// A dry-run removal only reads, so it must neither take the project lock nor be
// refused by one a rollback is holding.
func TestService_RemoveSavepoints_DryRunTakesNoLock(t *testing.T) {
	// Arrange
	locker := &mockLocker{} // no On("Acquire"): any call panics on the missing expectation
	repo := &mockSavepointRepo{}
	repo.On("List").Return([]model.Savepoint{{ID: "1"}}, nil)
	backend := &mockBackend{}
	svc := New(repo, locker, nil, nil, backend, nil)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []string{"1"}, DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	locker.AssertNotCalled(t, "Acquire")
	backend.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything)
}
