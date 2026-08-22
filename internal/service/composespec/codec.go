package composespec

import (
	"context"
	"errors"
	"fmt"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// specFilename names the artifact in compose's own error messages.
const specFilename = "arcave-savepoint.yaml"

// specOrigin carries what a compose document has no field for: the directory and
// files the project was loaded from, without which the docker CLI cannot find it.
const specOrigin = "x-arcave-origin"

const (
	originWorkingDir   = "working_dir"
	originComposeFiles = "compose_files"
)

// errNoProject is what Marshal answers a caller with nothing to render.
var errNoProject = errors.New("no compose project to render")

// Marshal renders the project as the compose document a savepoint commits beside
// its record.
func (a *Assembler) Marshal(project *types.Project) ([]byte, error) {
	if project == nil {
		return nil, errNoProject
	}

	stamped := *project
	stamped.Extensions = withOrigin(project.Extensions, project.WorkingDir, project.ComposeFiles)
	spec, err := stamped.MarshalYAML()
	if err != nil {
		return nil, fmt.Errorf("render compose spec: %w", err)
	}
	return spec, nil
}

// Parse reads an artifact back through compose's own loader, so a truncated or
// foreign document fails here rather than halfway through a rebuild.
func (a *Assembler) Parse(ctx context.Context, spec []byte) (*types.Project, error) {
	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{{Filename: specFilename, Content: spec}},
	}, func(o *loader.Options) {
		// The artifact is already resolved: a `$` in an env value is data, and no
		// env_file of the machine that wrote it is reachable from here.
		o.SkipInterpolation = true
		o.SkipResolveEnvironment = true
		o.ResolvePaths = false
	})
	if err != nil {
		return nil, fmt.Errorf("load compose spec: %w", err)
	}

	project.WorkingDir, project.ComposeFiles = readOrigin(project.Extensions)
	return project, nil
}

// withOrigin stamps the origin onto a copy of the extensions, replacing whatever
// an earlier render left there.
func withOrigin(extensions types.Extensions, workingDir string, files []string) types.Extensions {
	stamped := types.Extensions{}
	for key, value := range extensions {
		if key != specOrigin {
			stamped[key] = value
		}
	}

	origin := map[string]any{}
	if workingDir != "" {
		origin[originWorkingDir] = workingDir
	}
	if len(files) > 0 {
		origin[originComposeFiles] = files
	}
	if len(origin) > 0 {
		stamped[specOrigin] = origin
	}

	if len(stamped) == 0 {
		return nil
	}
	return stamped
}

// readOrigin reads the origin back; yaml renders the file list as []any, so both
// shapes are accepted.
func readOrigin(extensions types.Extensions) (workingDir string, files []string) {
	origin, ok := extensions[specOrigin].(map[string]any)
	if !ok {
		return "", nil
	}

	if dir, ok := origin[originWorkingDir].(string); ok {
		workingDir = dir
	}
	switch list := origin[originComposeFiles].(type) {
	case []string:
		files = list
	case []any:
		for _, entry := range list {
			if file, ok := entry.(string); ok {
				files = append(files, file)
			}
		}
	}
	return workingDir, files
}
