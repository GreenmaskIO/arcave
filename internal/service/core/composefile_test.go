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
	"errors"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// newGeneratedProject returns two services, one bind under the working directory.
func newGeneratedProject() *types.Project {
	return &types.Project{Name: demoProject, WorkingDir: "/srv/demo", Services: types.Services{
		"web": {Name: "web"},
		"db": {Name: "db", Volumes: []types.ServiceVolumeConfig{
			{Type: types.VolumeTypeBind, Source: "/srv/demo/conf", Target: "/etc/conf"},
		}},
	}}
}

// workingDirIs matches a project whose working directory, the base of its relative binds, is want.
func workingDirIs(want string) any {
	return mock.MatchedBy(func(project *types.Project) bool {
		return project.WorkingDir == want
	})
}

func TestComposeFileService_GenerateCompose(t *testing.T) {
	tests := []struct {
		name           string
		req            model.GenerateComposeRequest
		wantWorkingDir string
	}{
		{name: "relative to the working dir", req: model.GenerateComposeRequest{}, wantWorkingDir: "/srv/demo"},
		{name: "relative to the output dir", req: model.GenerateComposeRequest{Dir: "/srv"}, wantWorkingDir: "/srv"},
		{name: "absolute", req: model.GenerateComposeRequest{AbsolutePaths: true, Dir: "/srv"}, wantWorkingDir: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			introspection := demoIntrospection()
			spec := []byte("name: demo\n")
			inspector := &mockInspector{}
			assembler := &mockAssembler{}
			codec := &mockCodec{}
			introspect := inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
			assemble := assembler.On("Assemble", mock.Anything, introspection).Return(newGeneratedProject(), nil).Once()
			marshal := codec.On("Marshal", workingDirIs(tt.wantWorkingDir)).Return(spec, nil).Once()
			parse := codec.On("Parse", mock.Anything, spec).Return(newGeneratedProject(), nil).Once()
			mock.InOrder(introspect, assemble, marshal, parse)
			svc := NewComposeFileService(ComposeFileDeps{ComposeInspector: inspector, ProjectAssembler: assembler, SpecCodec: codec})
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			res, err := svc.GenerateCompose(ctx, tt.req)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, model.GenerateComposeResponse{
				ComposeProject: demoProject,
				Services:       []string{"db", "web"},
				Spec:           spec,
			}, res)
			inspector.AssertExpectations(t)
			assembler.AssertExpectations(t)
			codec.AssertExpectations(t)
		})
	}
}

// A compose project with no container has nothing to assemble a file from.
func TestComposeFileService_GenerateCompose_ComposeProjectAbsent(t *testing.T) {
	// Arrange
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(demoAbsentIntrospection(), nil).Once()
	assembler := &mockAssembler{}
	codec := &mockCodec{}
	svc := NewComposeFileService(ComposeFileDeps{ComposeInspector: inspector, ProjectAssembler: assembler, SpecCodec: codec})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.GenerateCompose(ctx, model.GenerateComposeRequest{})

	// Assert
	require.ErrorIs(t, err, model.ErrComposeProjectAbsent)
	assert.Contains(t, err.Error(), `compose project "demo"`)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
}

// A file compose's loader refuses is never handed to the user.
func TestComposeFileService_GenerateCompose_ParseFails(t *testing.T) {
	// Arrange
	introspection := demoIntrospection()
	spec := []byte("name: demo\n")
	cause := errors.New("bad spec")
	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything).Return(introspection, nil).Once()
	assembler := &mockAssembler{}
	assembler.On("Assemble", mock.Anything, introspection).Return(newGeneratedProject(), nil).Once()
	codec := &mockCodec{}
	codec.On("Marshal", mock.Anything).Return(spec, nil).Once()
	codec.On("Parse", mock.Anything, spec).Return(nil, cause).Once()
	svc := NewComposeFileService(ComposeFileDeps{ComposeInspector: inspector, ProjectAssembler: assembler, SpecCodec: codec})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.GenerateCompose(ctx, model.GenerateComposeRequest{})

	// Assert
	require.ErrorIs(t, err, cause)
	assert.Zero(t, res)
	inspector.AssertExpectations(t)
	assembler.AssertExpectations(t)
	codec.AssertExpectations(t)
}
