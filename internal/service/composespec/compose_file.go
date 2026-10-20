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

package composespec

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// composeFileName names the user's compose file in compose's own error messages.
const composeFileName = "docker-compose.yaml"

// ComposeFileCodec moves a compose project through the compose file a user runs: no arcave
// extension, every `$` escaped, since compose interpolates what Codec keeps as data, and bind
// sources relative to the project's WorkingDir, the directory the file lives in.
type ComposeFileCodec struct{}

func NewComposeFileCodec() *ComposeFileCodec { return &ComposeFileCodec{} }

// Marshal renders the project as a compose file, escaping `$` as `docker compose config` does; a
// bind source under WorkingDir reads as ./<rel>, and an empty WorkingDir leaves every one absolute.
func (s *ComposeFileCodec) Marshal(project *types.Project) ([]byte, error) {
	if project == nil {
		return nil, errNoProject
	}

	composeFile := *withRelativeBinds(project)
	composeFile.Extensions = types.Extensions{}
	for key, value := range project.Extensions {
		if key != specOrigin {
			composeFile.Extensions[key] = value
		}
	}
	spec, err := composeFile.MarshalYAML()
	if err != nil {
		return nil, fmt.Errorf("render compose file: %w", err)
	}
	return bytes.ReplaceAll(spec, []byte("$"), []byte("$$")), nil
}

// Parse loads a compose file as `docker compose` does, interpolation included, against an empty
// environment: a value that still names a variable reads back changed.
func (s *ComposeFileCodec) Parse(ctx context.Context, spec []byte) (*types.Project, error) {
	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{{Filename: composeFileName, Content: spec}},
		Environment: types.Mapping{},
	}, func(o *loader.Options) {
		// A relative bind names the directory the file lands in, which is not known here.
		o.ResolvePaths = false
	})
	if err != nil {
		return nil, fmt.Errorf("load compose file: %w", err)
	}
	return project, nil
}

// withRelativeBinds returns a copy of project whose bind sources under its WorkingDir read as
// ./<rel>; a source outside it stays absolute.
func withRelativeBinds(project *types.Project) *types.Project {
	dir := project.WorkingDir
	if dir == "" {
		return project
	}

	relative := *project
	relative.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		service.Volumes = slices.Clone(service.Volumes)
		for i, volume := range service.Volumes {
			if volume.Type != types.VolumeTypeBind {
				continue
			}
			if source, ok := toRelativePath(dir, volume.Source); ok {
				service.Volumes[i].Source = source
			}
		}
		relative.Services[name] = service
	}
	return &relative
}

// toRelativePath spells path as ./<rel> to dir, or reports false for one outside it.
func toRelativePath(dir, path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	return "." + string(filepath.Separator) + rel, true
}
