package introspect

import (
	"context"
	"fmt"
	"sort"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/arcave/internal/model"
)

type composeProjectLoader interface {
	Load(ctx context.Context, files ...string) (*types.Project, error)
}

// Inspector answers one question — "what does this compose project declare?" —
// and answers it from the files alone, with no daemon and no side effects. It is
// stateless across calls: the compose files are supplied per call, so one
// instance is shared across invocations.
//
// Its one field is the loader seam. Inspect resolves the project through load,
// which NewInspector wires to Load; a test overrides it with a hand-built
// *types.Project so the projection (mountsOf, and the Inspection it assembles)
// is exercised without a compose file on disk. Load itself stays an exported
// package function — client/docker's ComposeRunner calls it directly, one layer
// over, so that path needs no seam and does not go through the field.
type Inspector struct {
	// load resolves the compose project from files. NewInspector wires it to
	// Load; a test swaps in a fake to drive Inspect off an in-memory project.
	loader composeProjectLoader
}

// NewInspector returns a compose-file inspector.
func NewInspector(loader composeProjectLoader) *Inspector {
	return &Inspector{loader: loader}
}

// Inspect loads the compose project described by files and returns its name and
// the mounts arcave should snapshot. See Load for how files is resolved.
func (c *Inspector) Inspect(ctx context.Context, files ...string) (model.Inspection, error) {
	project, err := c.loader.Load(ctx, files...)
	if err != nil {
		return model.Inspection{}, fmt.Errorf("loading docker compose file(s): %w", err)
	}
	mounts, skipped := mountsOf(project)
	return model.Inspection{
		ComposeProject: project.Name,
		Mounts:         mounts,
		Skipped:        skipped,
	}, nil
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
