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

// Package ociimage is the oci-images collector: stored upstream images no record names.
package ociimage

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/utils"
)

// Collector finds stored upstream images no committed record names.
type Collector struct{}

func New() *Collector { return &Collector{} }

func (s *Collector) Kind() model.GarbageCollectorKind {
	return model.GarbageCollectorKindOCIImages
}

func (s *Collector) DataKind() model.GarbageDataKind {
	return model.GarbageDataKindUpstreamImages
}

// Find names every stored image no record names, held back while a capture is live or unknown.
func (s *Collector) Find(ctx context.Context, scope gc.ComposeProjectScope) (model.GarbageStat, error) {
	var (
		stored    = imagecopy.NewStoredImages(scope.Objects)
		live      = gc.NewLiveImages(scope.Savepoints)
		findings  = gc.NewFindings()
		liveCount int
	)
	for _, image := range stored {
		switch _, isLive := live[image.ID]; {
		case isLive:
			liveCount++
		case gc.HoldForCaptures(&findings, scope, image.Key, image.LastModified):
		default:
			findings.Ops = append(findings.Ops, model.GarbageOp{
				Action:       model.GarbageActionDeleteTree,
				Target:       image.Key,
				Bytes:        utils.New(image.Bytes),
				Reason:       model.GarbageReasonUnreferenced,
				LastModified: image.LastModified,
			})
		}
	}
	return model.OCIImageStat{
		GarbageFindings: findings,
		Live:            liveCount,
	}, nil
}
