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

	"github.com/greenmaskio/arcave/internal/config"
)

// configWriter persists the project config. Only Init uses it: every other
// command takes a resolved config.Config by value.
type configWriter interface {
	Exists() (bool, error)
	Save(config.Config) error
	Path() string
}

// InitService creates a project's arcave metadata. It writes one file and holds
// nothing else: no storage, no snapshot backend, no daemon and no compose project.
type InitService struct {
	projectRoot  string
	configWriter configWriter
}

// NewInitService wires an InitService for the project rooted at projectRoot.
func NewInitService(projectRoot string, writer configWriter) *InitService {
	return &InitService{projectRoot: projectRoot, configWriter: writer}
}

// Init writes cfg to .arcave/config.yaml verbatim, returning the resolved project
// name and the file's path. An initialised project is an error unless override.
func (s *InitService) Init(_ context.Context, cfg config.Config, override bool) (resolvedName, path string, err error) {
	exists, err := s.configWriter.Exists()
	if err != nil {
		return "", "", fmt.Errorf("check existing config: %w", err)
	}
	if exists && !override {
		return "", "", fmt.Errorf("project already initialized: %s exists (use --override to overwrite)", s.configWriter.Path())
	}

	// Re-applied here because an MCP caller may hand over a Config it assembled by hand.
	cfg.ApplyDefaults(s.projectRoot)

	if err := s.configWriter.Save(cfg); err != nil {
		return "", "", fmt.Errorf("saving config.yaml file: %w", err)
	}
	return cfg.Project.Name, s.configWriter.Path(), nil
}
