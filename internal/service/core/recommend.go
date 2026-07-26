package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

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

// composeServiceInspector reads what each compose service runs and what it
// already mounts. It is a seam of its own rather than a method added to
// composeInspector: RecommendService needs only this projection, and the
// savepoint workflows need only the other, so neither depends on the half it
// does not use.
type composeServiceInspector interface {
	Services(ctx context.Context, files ...string) ([]model.ComposeService, error)
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
	composeInspector composeServiceInspector
}

// NewRecommendService wires a recommend service.
func NewRecommendService(catalog recommendationCatalog, ci composeServiceInspector) *RecommendService {
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

	services, err := s.composeInspector.Services(ctx, composeFiles...)
	if err != nil {
		return nil, fmt.Errorf("inspecting compose services: %w", err)
	}

	recommendations := make([]model.Recommendation, 0)
	for _, svc := range services {
		// A service that only builds, or whose image would not parse, has no key
		// to match on. It is not an error: arcave cannot know what such an image
		// stores, so it says nothing about it.
		if svc.ImageRepo == "" {
			continue
		}
		entry, ok := index[svc.ImageRepo]
		if !ok {
			continue
		}

		covered := make(map[string]struct{}, len(svc.MountTargets))
		for _, target := range svc.MountTargets {
			covered[target] = struct{}{}
		}
		for _, volume := range entry.Volumes {
			if _, ok := covered[volume]; ok {
				continue
			}
			recommendations = append(recommendations, newRecommendation(svc.Name, svc.Image, volume))
		}
	}
	return recommendations, nil
}

// operationAddVolume is the value newRecommendation stamps on every
// recommendation it mints: a persistent volume the image wants and the compose
// file does not declare. It sits with its one reader rather than in model —
// nothing outside this service names it, and a constant reaches model only when a
// layer that *cannot* import its owner has to. Should `arcave fix` land in an
// adapter and need to dispatch on the value, exporting it is the smaller step.
//
// It is unexported for the same reason snapshotKey is: a seam nobody outside
// pulls on should not be exported ahead of a caller.
const operationAddVolume = "AddVolume"

// newRecommendation returns an AddVolume recommendation with its hash filled in.
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
