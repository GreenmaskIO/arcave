// Package compose reads the compose project's *declared* state: it wraps the
// Compose specification SDK (github.com/compose-spec/compose-go) to parse,
// interpolate and validate one or more docker-compose.yml files, and reports the
// project name plus the persistent mounts GM snapshots.
//
// It never contacts the Docker daemon — its only dependency is the compose-go
// parser — which is why `gm init` (whose whole job is to verify the project
// loads) runs with docker stopped. Everything that touches the *running* stack
// lives in internal/client/docker instead: container/volume I/O through the
// Docker SDK, and the compose lifecycle through the docker CLI.
package compose

import (
	"context"
	"fmt"
	"sort"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/greenmaskio/gm-tools/internal/model"
)

// Inspector answers one question — "what does this compose project declare?" —
// and answers it from the files alone, with no daemon and no side effects. It is
// stateless: the compose files are supplied per call, so one instance is shared
// across invocations.
type Inspector struct{}

// NewInspector returns a compose-file inspector.
func NewInspector() *Inspector { return &Inspector{} }

// Inspect loads the compose project described by files and returns its name and
// the mounts GM should snapshot. See load for how files is resolved.
func (c *Inspector) Inspect(ctx context.Context, files ...string) (model.Inspection, error) {
	project, name, err := load(ctx, files...)
	if err != nil {
		return model.Inspection{}, fmt.Errorf("loading docker compose file(s): %w", err)
	}
	mounts, skipped := mountsOf(project)
	return model.Inspection{
		ComposeProject: name,
		Mounts:         mounts,
		Skipped:        skipped,
	}, nil
}

// load reads and verifies the Docker Compose project described by files.
//
// files is the ordered list of Compose configuration paths (the main
// docker-compose.yml plus any overrides). When empty, compose-go resolves the
// standard compose file names in the working directory, honouring the
// COMPOSE_FILE environment variable.
//
// The returned project has been parsed, interpolated, and validated against the
// Compose specification. Load additionally verifies that the project defines at
// least one service, since GM has nothing to manage otherwise.
//
// The second return value is the resolved Compose project name (from an
// explicit name, the top-level `name:` field, COMPOSE_PROJECT_NAME, or the
// working directory).
func load(ctx context.Context, files ...string) (*types.Project, string, error) {
	opts, err := cli.NewProjectOptions(
		files,
		cli.WithOsEnv,
		cli.WithDotEnv,
		cli.WithConfigFileEnv, // COMPOSE_FILE
		cli.WithDefaultConfigPath,
	)
	if err != nil {
		return nil, "", fmt.Errorf("build compose project options: %w", err)
	}

	project, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("load compose project: %w", err)
	}

	if len(project.Services) == 0 {
		return nil, "", fmt.Errorf("compose project %q defines no services", project.Name)
	}

	return project, project.Name, nil
}

// mountsOf returns every persistent mount of every service in the project —
// named volumes, bind mounts, and anonymous volumes — keyed by service and
// container target. It also returns the mounts it had to skip (tmpfs and the
// like) so callers can warn about state that will not be captured. Services are
// visited in sorted name order so the output is deterministic.
func mountsOf(project *types.Project) ([]model.Mount, []model.SkippedMount) {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var (
		mounts  []model.Mount
		skipped []model.SkippedMount
	)
	for _, service := range names {
		for _, v := range project.Services[service].Volumes {
			switch v.Type {
			case "volume":
				source := "" // anonymous volume; resolved from the container later
				if v.Source != "" {
					source = volumeName(project, v.Source)
				}
				mounts = append(mounts, model.Mount{
					Service: service,
					Target:  v.Target,
					Type:    "volume",
					Source:  source,
				})
			case "bind":
				mounts = append(mounts, model.Mount{
					Service: service,
					Target:  v.Target,
					Type:    "bind",
					Source:  v.Source,
				})
			default:
				skipped = append(skipped, model.SkippedMount{
					Service: service,
					Reason:  fmt.Sprintf("%s mount at %q", v.Type, v.Target),
				})
			}
		}
	}
	return mounts, skipped
}

// volumeName resolves the Docker volume name for a compose volume key. Compose
// prefixes volume keys with the project name unless the volume declares an
// explicit name or is marked external (compose-go populates Name in both cases).
func volumeName(project *types.Project, key string) string {
	if v, ok := project.Volumes[key]; ok && v.Name != "" {
		return v.Name
	}
	return project.Name + "_" + key
}
