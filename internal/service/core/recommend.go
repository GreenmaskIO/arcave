package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/distribution/reference"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
)

// recommendationCatalog reads the Arcave Recommender Config — the images arcave
// knows the persistent state of, and where that state lives.
//
// List takes no context, unlike every other repository seam here: the catalog is
// local bytes, half of them embedded in the binary, so there is no call behind it
// that could be cancelled or cross a network.
type recommendationCatalog interface {
	List() ([]model.ARCEntry, error)
}

// composeIntrospector reads what a compose project declares. Every consumer
// takes the whole introspection and projects out the part it cares about — the
// savepoint workflows read mounts, this service reads images and mount targets —
// so the inspector stays a reader of compose files and nothing else.
type composeIntrospector interface {
	Introspect(ctx context.Context, files ...string) (model.IntrospectionResult, error)
}

// RecommendService advises on state a compose project would lose: a service
// whose image writes into the container filesystem, with nothing persistent
// mounted where it writes.
//
// It is strictly read-only — it parses compose files and a catalog, and that is
// all. So, like InitService, it needs neither storage, nor a snapshot backend,
// nor the Docker engine, and it takes no project lock: there is nothing here to
// serialise against a running rollback.
type RecommendService struct {
	catalog          recommendationCatalog
	composeInspector composeIntrospector
}

// NewRecommendService wires a recommend service.
func NewRecommendService(catalog recommendationCatalog, ci composeIntrospector) *RecommendService {
	return &RecommendService{catalog: catalog, composeInspector: ci}
}

// Recommend returns the volumes the project's services should mount and do not.
// A service is matched to a catalog entry by its normalized image repository —
// exactly, or through one of the entry's aliases — and each of that entry's
// volumes that no persistent mount already covers becomes one recommendation.
//
// The result is never nil, so an adapter marshals an empty result as `[]` rather
// than `null`. Its order is deterministic: services sorted by name (the inspector
// sorts them), volumes in catalog order.
func (s *RecommendService) Recommend(ctx context.Context, composeFiles ...string) ([]model.Recommendation, error) {
	entries, err := s.catalog.List()
	if err != nil {
		return nil, fmt.Errorf("reading recommender catalog: %w", err)
	}
	index := catalogIndex(entries)

	result, err := s.composeInspector.Introspect(ctx, composeFiles...)
	if err != nil {
		return nil, fmt.Errorf("inspecting compose services: %w", err)
	}

	recommendations := make([]model.Recommendation, 0)
	for _, svc := range result.Project.Services {
		repo, err := imageRepo(svc.Image)
		if err != nil {
			log.Ctx(ctx).Warn().Err(err).Str("service", svc.Name).Str("image", svc.Image).
				Msg("cannot parse service image; skipping it when matching the recommender catalog")
		}
		// A service that only builds, or whose image would not parse, has no key
		// to match on. It is not an error: arcave cannot know what such an image
		// stores, so it says nothing about it.
		if repo == "" {
			continue
		}
		entry, ok := index[repo]
		if !ok {
			continue
		}

		covered := coveredTargets(svc)
		for _, volume := range entry.Volumes {
			if _, ok := covered[volume]; ok {
				continue
			}
			recommendations = append(recommendations, newRecommendation(svc.Name, svc.Image, volume))
		}
	}
	return recommendations, nil
}

// dockerHubDomain is the registry reference.ParseNormalizedNamed fills in for an
// unqualified image. It covers one quirk only: Docker Hub alone injects a
// `library/` namespace nobody writes.
const dockerHubDomain = "docker.io"

// imageRepo reduces a compose image reference to the repository it names, the key
// the catalog is written in: no tag, no digest, no `docker.io/library/` prefix.
// `postgres:16` becomes `postgres`, `ghcr.io/org/pg:1` becomes `org/pg`.
//
// The registry is dropped rather than matched on, so every registry is treated
// alike. A mirror that *prefixes* the path does not fall out of this, and the fix
// is a catalog alias: trimming leading segments until something matched would
// advise on anyone's own image named `redis`.
//
// A service that only builds declares no image and resolves to "", which is not
// an error. An unparseable reference is, and comes back with the reason.
func imageRepo(image string) (string, error) {
	if image == "" {
		return "", nil
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("parse image reference %q: %w", image, err)
	}
	path := reference.Path(reference.TrimNamed(named))
	// Docker Hub's official images live under `library/`, which no compose file
	// writes and the catalog therefore does not spell either.
	if reference.Domain(named) == dockerHubDomain {
		path = strings.TrimPrefix(path, "library/")
	}
	return path, nil
}

// coveredTargets is the set of container paths the service already mounts
// something persistent at. A tmpfs stores nothing and an image mount is
// read-only, so neither may read as a path already taken care of.
//
// It reads the inspector's classification rather than compose's raw mount type,
// which is what keeps this projection and the savepoint workflows' from
// disagreeing: an anonymous volume persists just as a named one does, and both
// are compose's "volume".
func coveredTargets(svc model.Service) map[string]struct{} {
	covered := make(map[string]struct{}, len(svc.Volumes))
	for _, v := range svc.Volumes {
		switch v.Type {
		case model.VolumeTypeNamed, model.VolumeTypeAnon, model.VolumeTypeBind:
			covered[v.Target] = struct{}{}
		}
	}
	return covered
}

// newRecommendation returns an add-volume recommendation with its hash filled in.
// It is the only way one is minted, so the canonical form the hash is taken over
// — `service|image|volume|operation`, sha256, lowercase hex — has exactly one
// implementation and cannot drift.
//
// image is the value the compose file declares, verbatim (tag and all), rather
// than the normalized repository the catalog was matched on: the hash must change
// when the user bumps the image, since the advice may no longer apply.
func newRecommendation(service, image, volume string) model.Recommendation {
	r := model.Recommendation{
		Service:   service,
		Image:     image,
		Volume:    volume,
		Operation: model.RecommendationOperationAddVolume,
	}
	sum := sha256.Sum256([]byte(r.Service + "|" + r.Image + "|" + r.Volume + "|" + string(r.Operation)))
	r.Hash = hex.EncodeToString(sum[:])
	return r
}

// catalogIndex maps every key an entry answers to — its image and each of its
// aliases — onto the entry itself. Later entries win, which is what gives a
// user-supplied catalog the last word: it is merged after the built-in one, so an
// alias it reassigns overrides arcave's own mapping.
func catalogIndex(entries []model.ARCEntry) map[string]model.ARCEntry {
	index := make(map[string]model.ARCEntry, len(entries))
	for _, e := range entries {
		index[e.Image] = e
		for _, alias := range e.Aliases {
			index[alias] = e
		}
	}
	return index
}
