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

// Collector finds the arcave project's arcave/…:sp-<savepoint> tags no container,
// running or not, was created from.
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
// by image; a tag of no origin or of another compose project is only counted.
func (s *Collector) Find(ctx context.Context, scope gc.DaemonScope) (model.GarbageStat, error) {
	var (
		tagged               []model.TaggedImage
		containers           []model.ContainerImage
		usedRefs             = map[model.ImageRef]bool{}
		usedImages           = map[model.ImageDigestID]bool{}
		ops                  = []model.GarbageOp{}
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
		}
		ops = append(ops, model.GarbageOp{
			Action: model.GarbageActionUntag,
			Target: string(image.Ref),
			Reason: model.GarbageReasonUnusedTag,
		})
	}
	return model.DockerLayerTagStat{
		GarbageFindings:      model.GarbageFindings{Ops: ops},
		InUse:                inUse,
		Unattributed:         unattributed,
		OtherComposeProjects: otherComposeProjects,
	}, nil
}
