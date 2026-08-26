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

package core

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
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
				_, err := svc.Rollback(ctx, model.RollbackRequest{Latest: ptrBool(true)})
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
			svc := New(Deps{
				VolumeRemover: &mockVolumeRemover{},
				SavepointRepo: repo, ProjectLocker: locker, ComposeInspector: inspector,
				ComposeRunner: runner, BackendRegistry: registry, Inflight: &fakeInflight{},
				Store: nil, DefaultBackend: model.SnapshotBackendCopy,
			})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			err := tt.call(svc, ctx)

			// Assert
			require.ErrorIs(t, err, model.ErrProjectLocked)
			// A lock that was never taken must not be released, and the locker carries
			// no Release expectation — a release would fail the run as an unexpected call.
			locker.AssertExpectations(t)
			repo.AssertExpectations(t)
			inspector.AssertExpectations(t)
			runner.AssertExpectations(t)
			registry.AssertExpectations(t)
		})
	}
}
