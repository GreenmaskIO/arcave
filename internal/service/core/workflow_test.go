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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// The live compose project is acted on as its artifact parses back, not as the
// assembler built it: a revert rebuilds from the same loader a rollback does.
func TestService_prepare(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoIntrospection(), nil).Once()
	assembler := &mockAssembler{}
	codec := &mockCodec{}
	mock.InOrder(
		assembler.On("Assemble", mock.Anything, demoIntrospection()).Return(demoAssembled, nil).Once(),
		codec.On("Marshal", demoAssembled).Return(demoSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, demoSpecBytes).Return(demoSpec, nil).Once(),
	)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return(nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(&mockBackend{volume: demoVolume()}, nil).Once()

	svc := New(Deps{
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Store:            store,
		DefaultBackend:   model.SnapshotBackendCopy,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	ws, err := svc.prepare(ctx, 0)
	require.NoError(t, err)
	err = svc.stopServices(ctx, ws)

	// Assert
	require.NoError(t, err)
	assert.Same(t, demoSpec, ws.project)
	assert.Equal(t, demoSpecBytes, ws.spec)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}
