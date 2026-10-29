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
	"bytes"
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
	introspection := demoIntrospection()
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	assembler := &mockAssembler{}
	codec := &mockCodec{}
	mock.InOrder(
		assembler.On("Assemble", mock.Anything, introspection).Return(demoAssembled, nil).Once(),
		codec.On("Marshal", demoAssembled).Return(demoSpecBytes, nil).Once(),
		codec.On("Parse", mock.Anything, demoSpecBytes).Return(demoSpec, nil).Once(),
	)
	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, demoSpec).Return([]string{demoContainerID}, nil).Once()

	store := newDirStore(t)
	registry := &mockRegistry{}
	registry.On("New", matchSpec(store, "volume-named-demo_db")).Return(&mockBackend{volume: demoVolume()}, nil).Once()

	svc := New(Deps{
		SessionLocker:    fakeSessionLocker{},
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		ComposeRunner:    runner,
		BackendRegistry:  registry,
		Store:            store,
		CaptureDefaults:  demoCaptureDefaults,
		MinDaemonMajor:   25,
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	working, err := svc.prepare(ctx, 0)
	require.NoError(t, err)
	killed, err := svc.stopServices(ctx, working, true)

	// Assert
	require.NoError(t, err)
	assert.Same(t, demoSpec, working.project)
	assert.Equal(t, demoSpecBytes, working.spec)
	assert.Equal(t, []string{demoContainerID}, killed)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
	runner.AssertExpectations(t)
	registry.AssertExpectations(t)
}

func TestNewReplicaGaps(t *testing.T) {
	tests := []struct {
		name    string
		numbers []int
		want    string
		wantGap []model.ReplicaGap
	}{
		{name: "no gap", numbers: []int{2, 1, 3}},
		{name: "a gap in the middle", numbers: []int{3, 1},
			want:    `"service":"db","replicas":[1,3],"missing":[2],"message":"some replicas of this service are gone`,
			wantGap: []model.ReplicaGap{{Service: demoService, Replicas: []int{1, 3}, Missing: []int{2}}}},
		{name: "the first replica gone", numbers: []int{2, 3},
			want:    `"service":"db","replicas":[2,3],"missing":[1],"message":"some replicas of this service are gone`,
			wantGap: []model.ReplicaGap{{Service: demoService, Replicas: []int{2, 3}, Missing: []int{1}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoIntrospection()
			svc := model.Service{Name: demoService}
			for _, n := range tt.numbers {
				svc.Containers = append(svc.Containers, model.Container{Number: n})
			}
			introspection.Project.Services = []model.Service{svc}
			logs := &syncBuffer{}
			ctx := zerolog.New(logs).WithContext(context.Background())

			// Act
			gaps := newReplicaGaps(ctx, introspection)

			// Assert
			assert.Equal(t, tt.wantGap, gaps)
			if tt.want == "" {
				assert.Empty(t, logs.String())
				return
			}
			assert.Contains(t, logs.String(), tt.want)
		})
	}
}

func TestLogInterrupt(t *testing.T) {
	tests := []struct {
		name        string
		interrupted bool
		want        string
	}{
		{name: "a live context logs nothing"},
		{name: "an interrupted context logs the action", interrupted: true,
			want: `{"level":"info","message":"restarting services"}` + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var buf bytes.Buffer
			ctx, cancel := context.WithCancel(zerolog.New(&buf).WithContext(context.Background()))
			defer cancel()
			if tt.interrupted {
				cancel()
			}

			// Act
			logInterrupt(ctx, "restarting services")

			// Assert
			assert.Equal(t, tt.want, buf.String())
		})
	}
}
