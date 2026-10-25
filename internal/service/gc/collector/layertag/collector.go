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

// Package layertag is the docker-layer-tags collector: restored layer tags no container uses.
package layertag

import (
	"context"
	"slices"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc"
)

// probeTimeout bounds the check that the daemon answers.
const probeTimeout = 10 * time.Second

// layerDaemon is the daemon as the collector reads it.
type layerDaemon interface {
	Ping(ctx context.Context) error
	ListLayerImages(ctx context.Context) ([]model.TaggedImage, error)
	ListContainerImages(ctx context.Context) ([]model.ContainerImage, error)
}

// Collector finds the arcave project's arcave/…:sp-<id> tags no container, running or
// not, was created from.
type Collector struct {
	daemon        layerDaemon
	arcaveProject model.ArcaveProjectName
}

// New reads daemon for arcaveProject's tags.
func New(daemon layerDaemon, arcaveProject model.ArcaveProjectName) *Collector {
	return &Collector{
		daemon:        daemon,
		arcaveProject: arcaveProject,
	}
}

func (s *Collector) Kind() model.GarbageCollectorKind {
	return model.GarbageCollectorKindDockerLayerTags
}

func (s *Collector) DataKind() model.GarbageDataKind {
	return model.GarbageDataKindRestoredLayers
}

// Probe checks, within probeTimeout, that the daemon answers.
func (s *Collector) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return s.daemon.Ping(ctx)
}

// Find names every layer tag of the scope's compose projects no container uses, by reference or
// by image, holding back one tagged within the scope's grace; a tag of no origin or of another
// compose project is only counted.
func (s *Collector) Find(ctx context.Context, scope gc.DaemonScope) (model.GarbageStat, error) {
	var (
		tagged               []model.TaggedImage
		containers           []model.ContainerImage
		usedRefs             = map[model.ImageRef]bool{}
		usedImages           = map[model.ImageDigestID]bool{}
		findings             = model.GarbageFindings{Ops: []model.GarbageOp{}}
		inUse                int
		unattributed         int
		otherComposeProjects int
		err                  error
	)
	if tagged, err = s.daemon.ListLayerImages(ctx); err != nil {
		return nil, err
	}
	if containers, err = s.daemon.ListContainerImages(ctx); err != nil {
		return nil, err
	}
	for _, container := range containers {
		usedRefs[container.Ref] = true
		usedImages[container.ImageID] = true
	}

	for _, image := range tagged {
		switch {
		case image.Origin == model.LayerOrigin{}:
			unattributed++
			continue
		case image.Origin.ArcaveProject != s.arcaveProject:
			continue
		case !slices.Contains(scope.ComposeProjects, image.Origin.ComposeProject):
			otherComposeProjects++
			continue
		case usedRefs[image.Ref] || usedImages[image.ImageID]:
			inUse++
			continue
		case holdRecent(&findings, image, scope.Now, scope.RestoredLayerGrace):
			// A rollback tags before its Create, and one in another project directory sharing
			// the arcave project tags under a lock this gc cannot see.
			continue
		}
		findings.Ops = append(findings.Ops, model.GarbageOp{
			Action:       model.GarbageActionUntag,
			Target:       string(image.Ref),
			Reason:       model.GarbageReasonUnusedTag,
			LastModified: image.TaggedAt,
		})
	}
	return model.DockerLayerTagStat{
		GarbageFindings:      findings,
		InUse:                inUse,
		Unattributed:         unattributed,
		OtherComposeProjects: otherComposeProjects,
	}, nil
}

// holdRecent keeps a tag out of the ops when it was tagged within the grace, or at no known
// time while there is one, and reports whether it did.
func holdRecent(findings *model.GarbageFindings, image model.TaggedImage, now time.Time, grace time.Duration) bool {
	switch {
	case grace <= 0:
		return false
	case image.TaggedAt.IsZero():
		findings.Unjudged = append(findings.Unjudged, model.GarbageSkip{
			Target: string(image.Ref),
			Reason: model.GarbageReasonTagTimeUnknown,
		})
	case image.TaggedAt.After(now.Add(-grace)):
		findings.Skipped = append(findings.Skipped, model.GarbageSkip{
			Target:       string(image.Ref),
			Reason:       model.GarbageReasonRecentlyTagged,
			LastModified: image.TaggedAt,
		})
	default:
		return false
	}
	return true
}
