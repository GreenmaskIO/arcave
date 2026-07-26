package introspect

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
)

// dockerHubDomain is the registry reference.ParseNormalizedNamed fills in for an
// unqualified image, and the one whose official images carry the `library/`
// namespace users never type.
//
// It is *not* a default registry, and arcave assumes no single one: imageRepo
// drops the domain of every reference, so a project pulling from Docker Hub, a
// corporate registry and ghcr.io at once matches all three against the same
// catalog. This constant covers one quirk only — Docker Hub is alone in
// injecting a namespace nobody writes.
const dockerHubDomain = "docker.io"

// Services returns every service the compose project declares: what it runs and
// which container paths it already mounts something persistent at. It is the
// second projection of the same loaded project Inspect works from, added rather
// than folded into model.Inspection so the savepoint workflows' view is
// untouched — they care about mounts, this cares about images.
func (c *Inspector) Services(ctx context.Context, files ...string) ([]model.ComposeService, error) {
	project, err := c.loader.Load(ctx, files...)
	if err != nil {
		return nil, fmt.Errorf("loading docker compose file(s): %w", err)
	}
	return servicesOf(ctx, project), nil
}

// servicesOf projects the loaded project into one ComposeService per service,
// in sorted name order so the output is deterministic.
//
// It applies the same persistence policy as mountsOf — volume and bind count,
// everything else does not — because the question here is the same one in
// reverse: a tmpfs mount at a path stores nothing, so it must not read as that
// path already being covered.
func servicesOf(ctx context.Context, project *types.Project) []model.ComposeService {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]model.ComposeService, 0, len(names))
	for _, name := range names {
		svc := project.Services[name]
		var targets []string
		for _, v := range svc.Volumes {
			switch v.Type {
			case "volume", "bind":
				targets = append(targets, v.Target)
			}
		}
		repo, err := imageRepo(svc.Image)
		if err != nil {
			log.Ctx(ctx).Warn().Err(err).Str("service", name).Str("image", svc.Image).
				Msg("cannot parse service image; skipping it when matching the recommender catalog")
		}
		services = append(services, model.ComposeService{
			Name:         name,
			Image:        svc.Image,
			ImageRepo:    repo,
			MountTargets: targets,
		})
	}
	return services
}

// imageRepo reduces a compose image reference to the repository it names, which
// is the key the recommender catalog is written in: no tag, no digest, and no
// `docker.io/library/` prefix. `postgres:16` becomes `postgres`,
// `ghcr.io/org/pg:1` becomes `org/pg`.
//
// The registry is dropped rather than matched on, so every registry a project
// pulls from is treated alike and `registry.corp.local/postgres:16` matches the
// same entry as `postgres:16`. The one case that does not fall out of this is a
// pull-through mirror that *prefixes* the path
// (`mycorp.jfrog.io/docker-remote/library/postgres`): the key it yields is
// genuinely different, and the fix is a catalog alias rather than a heuristic
// here — trimming leading path segments until something matched would advise on
// anyone's own image that happens to be named `redis`, and the catalog is
// additive, so a false positive could not be taken back.
//
// A service that only builds declares no image, which is not an error — it
// resolves to "" and the caller skips it. A reference that will not parse is: the
// repository cannot be named, so "" is returned alongside the reason, and the
// caller decides what to say about it.
//
// This is the one place distribution/reference is used, which is why
// model.ComposeService carries the normalized value: the seam above it stays free
// of the parsing library.
func imageRepo(image string) (string, error) {
	if image == "" {
		return "", nil
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("parse image reference %q: %w", image, err)
	}
	path := reference.Path(reference.TrimNamed(named))
	// Docker Hub's official images live under `library/`, a namespace nobody
	// writes in a compose file and the catalog therefore does not spell either.
	if reference.Domain(named) == dockerHubDomain {
		path = strings.TrimPrefix(path, "library/")
	}
	return path, nil
}
