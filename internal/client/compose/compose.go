// Package compose wraps the Compose specification SDK
// (github.com/compose-spec/compose-go) to read and validate Docker Compose
// projects from one or more configuration files.
package compose

import (
	"context"
	"fmt"
	"sort"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// Volume describes a single volume mount of a service.
type Volume struct {
	Type   string // volume, bind, tmpfs, ...
	Source string // named volume or host path ("" for anonymous volumes)
	Target string // mount path inside the container
}

// ServiceVolumes groups a service with all of its volume mounts.
type ServiceVolumes struct {
	Service string   // compose service name
	Image   string   // container image ("" for build-only services)
	Volumes []Volume // all volume mounts declared by the service
}

// Load reads and verifies the Docker Compose project described by files.
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
func Load(ctx context.Context, files ...string) (*types.Project, string, error) {
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

// GetVolumes returns, for every service in the project, all of its volume
// mounts (type, source and target). Results are sorted by service name so the
// output is deterministic across runs.
func GetVolumes(project *types.Project) []ServiceVolumes {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]ServiceVolumes, 0, len(names))
	for _, name := range names {
		mounts := project.Services[name].Volumes
		volumes := make([]Volume, 0, len(mounts))
		for _, v := range mounts {
			volumes = append(volumes, Volume{
				Type:   v.Type,
				Source: v.Source,
				Target: v.Target,
			})
		}
		result = append(result, ServiceVolumes{
			Service: name,
			Image:   project.Services[name].Image,
			Volumes: volumes,
		})
	}
	return result
}

// Mount describes one service mount GM snapshots, keyed by the service and the
// path it is mounted at inside the container — the stable key that works for
// named volumes, bind mounts, and anonymous volumes alike.
type Mount struct {
	Service string // compose service that owns the mount
	Target  string // mount path inside the container
	Type    string // "volume" or "bind"
	// Source identifies the underlying storage to clear on restore: a resolved
	// Docker volume name, a host path for bind mounts, or "" for an anonymous
	// volume (whose hashed name is resolved from the container at restore time).
	Source string
}

// SkippedMount describes a mount that cannot be snapshotted (tmpfs and other
// non-persistent mount types).
type SkippedMount struct {
	Service string
	Reason  string
}

// Mounts returns every persistent mount of every service in the project —
// named volumes, bind mounts, and anonymous volumes — keyed by service and
// container target. It also returns the mounts it had to skip (tmpfs and the
// like) so callers can warn about state that will not be captured.
func Mounts(project *types.Project) ([]Mount, []SkippedMount) {
	var (
		mounts  []Mount
		skipped []SkippedMount
	)
	for _, sv := range GetVolumes(project) {
		for _, v := range sv.Volumes {
			switch v.Type {
			case "volume":
				source := "" // anonymous volume; resolved from the container later
				if v.Source != "" {
					source = volumeName(project, v.Source)
				}
				mounts = append(mounts, Mount{
					Service: sv.Service,
					Target:  v.Target,
					Type:    "volume",
					Source:  source,
				})
			case "bind":
				mounts = append(mounts, Mount{
					Service: sv.Service,
					Target:  v.Target,
					Type:    "bind",
					Source:  v.Source,
				})
			default:
				skipped = append(skipped, SkippedMount{
					Service: sv.Service,
					Reason:  fmt.Sprintf("%s mount at %q", v.Type, v.Target),
				})
			}
		}
	}
	return mounts, skipped
}

// volumeName resolves the Docker volume name for a compose volume key. Compose
// prefixes volume keys with the project name unless the volume declares an
// explicit name or is marked external.
func volumeName(project *types.Project, key string) string {
	if v, ok := project.Volumes[key]; ok && v.Name != "" {
		return v.Name
	}
	return project.Name + "_" + key
}
