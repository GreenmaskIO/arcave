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

// Package composetools generates a compose file off a live compose project: a tool beside arcave's
// workflows, reaching the daemon alone.
package composetools

import (
	"context"
	"fmt"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/arcave/internal/model"
)

// composeIntrospector reports the live compose project it was bound to at construction.
type composeIntrospector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// projectAssembler builds the spec of a live compose project out of its introspection.
type projectAssembler interface {
	Assemble(ctx context.Context, introspection model.IntrospectionResult) (*types.Project, error)
}

// specCodec renders a compose project as a compose file and reads it back through compose's loader.
type specCodec interface {
	Marshal(project *types.Project) ([]byte, error)
	Parse(ctx context.Context, spec []byte) (*types.Project, error)
}

// Deps is what a Service is built from; SpecCodec renders the user's compose file.
type Deps struct {
	ComposeInspector composeIntrospector
	ProjectAssembler projectAssembler
	SpecCodec        specCodec
}

// Service generates the compose file of a live compose project; it only reads the daemon.
type Service struct {
	composeInspector composeIntrospector
	projectAssembler projectAssembler
	specCodec        specCodec
}

// New builds a Service from deps.
func New(deps Deps) *Service {
	return &Service{
		composeInspector: deps.ComposeInspector,
		projectAssembler: deps.ProjectAssembler,
		specCodec:        deps.SpecCodec,
	}
}

// GenerateCompose assembles the compose project as the daemon runs it now and renders it as
// a compose file compose's own loader reads back unchanged.
func (s *Service) GenerateCompose(
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

// requireComposeProject refuses a compose project the daemon holds no container of: there is
// nothing to assemble a spec from.
func requireComposeProject(introspection model.IntrospectionResult) error {
	if len(introspection.Project.Services) == 0 {
		return fmt.Errorf("compose project %q: %w", introspection.Project.Name, model.ErrComposeProjectAbsent)
	}
	return nil
}
