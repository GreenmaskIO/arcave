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

// Package inspection reads the live compose project off the daemon, under its own step, and
// refuses a daemon the provider cannot work on.
package inspection

import (
	"context"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// kindProjectIntrospect is the step an introspection runs under.
const kindProjectIntrospect progress.Kind = "project.introspect"

// composeIntrospector reads the live compose project as the daemon holds it.
type composeIntrospector interface {
	Introspect(ctx context.Context) (model.IntrospectionResult, error)
}

// Deps are Inspector's collaborators.
type Deps struct {
	ComposeInspector composeIntrospector
	// DaemonVersion is the version the engine reports; Inspect judges it.
	DaemonVersion string
	// MinDaemonMajor is the oldest major of that engine a workflow runs on; 0 requires none.
	MinDaemonMajor int
}

// Inspector reads one compose project as the daemon holds it.
type Inspector struct {
	composeInspector composeIntrospector
	daemonVersion    string
	minDaemonMajor   int
}

// New builds an Inspector from deps.
func New(deps Deps) *Inspector {
	return &Inspector{
		composeInspector: deps.ComposeInspector,
		daemonVersion:    deps.DaemonVersion,
		minDaemonMajor:   deps.MinDaemonMajor,
	}
}

// Inspect reads the compose project as the daemon holds it now, and refuses a daemon older than
// the provider runs on.
func (s *Inspector) Inspect(ctx context.Context) (model.IntrospectionResult, error) {
	introspection, err := s.Introspect(ctx)
	if err != nil {
		return model.IntrospectionResult{}, err
	}
	if err := requireDaemon(ctx, s.daemonVersion, s.minDaemonMajor); err != nil {
		return model.IntrospectionResult{}, err
	}
	return introspection, nil
}

// Introspect reads the compose project as the daemon holds it now, under its own step.
func (s *Inspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	ctx, step := progress.Run(ctx, progress.Step{Kind: kindProjectIntrospect})
	introspection, err := s.composeInspector.Introspect(ctx)
	step.End(err)
	if err != nil {
		return model.IntrospectionResult{}, fmt.Errorf("introspect compose project: %w", err)
	}
	return introspection, nil
}
