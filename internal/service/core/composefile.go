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
	"fmt"
	"slices"

	"github.com/greenmaskio/arcave/internal/model"
)

// ComposeFileDeps is what a ComposeFileService is built from; SpecCodec renders the user's
// compose file, not a savepoint's artifact.
type ComposeFileDeps struct {
	ComposeInspector composeIntrospector
	ProjectAssembler projectAssembler
	SpecCodec        specCodec
}

// ComposeFileService generates the compose file of a live compose project; it only reads the daemon.
type ComposeFileService struct {
	composeInspector composeIntrospector
	projectAssembler projectAssembler
	specCodec        specCodec
}

// NewComposeFileService builds a ComposeFileService from deps.
func NewComposeFileService(deps ComposeFileDeps) *ComposeFileService {
	return &ComposeFileService{
		composeInspector: deps.ComposeInspector,
		projectAssembler: deps.ProjectAssembler,
		specCodec:        deps.SpecCodec,
	}
}

// GenerateCompose assembles the compose project as the daemon runs it now and renders it as
// a compose file compose's own loader reads back unchanged.
func (s *ComposeFileService) GenerateCompose(
	ctx context.Context, req model.GenerateComposeRequest,
) (model.GenerateComposeResponse, error) {
	introspection, err := s.composeInspector.Introspect(ctx)
	if err != nil {
		return model.GenerateComposeResponse{}, fmt.Errorf("introspect compose project: %w", err)
	}
	if err := requireComposeProject(introspection); err != nil {
		return model.GenerateComposeResponse{}, err
	}

	assembled, err := s.projectAssembler.Assemble(ctx, introspection)
	if err != nil {
		return model.GenerateComposeResponse{}, fmt.Errorf("assembling the compose spec: %w", err)
	}
	// The working directory is what the codec spells bind sources relative to.
	switch {
	case req.AbsolutePaths:
		assembled.WorkingDir = ""
	case req.Dir != "":
		assembled.WorkingDir = req.Dir
	}
	spec, err := s.specCodec.Marshal(assembled)
	if err != nil {
		return model.GenerateComposeResponse{}, fmt.Errorf("rendering the compose file: %w", err)
	}
	// What the user runs next is compose's loader, so a file it reads otherwise fails here.
	project, err := s.specCodec.Parse(ctx, spec)
	if err != nil {
		return model.GenerateComposeResponse{}, fmt.Errorf("parsing the compose file: %w", err)
	}

	services := project.ServiceNames()
	slices.Sort(services)
	return model.GenerateComposeResponse{
		ComposeProject: model.DockerComposeProjectName(introspection.Project.Name),
		Services:       services,
		Spec:           spec,
	}, nil
}
