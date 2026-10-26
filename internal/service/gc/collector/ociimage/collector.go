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
	"fmt"

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

// Find names every stored image no record names.
func (s *Collector) Find(ctx context.Context, scope gc.ComposeProjectScope) (model.GarbageStat, error) {
	var (
		stored    = imagecopy.NewStoredImages(scope.Objects)
		live      = gc.NewLiveImages(scope.Savepoints)
		ops       = []model.GarbageOp{}
		liveCount int
	)
	for _, image := range stored {
		if _, isLive := live[image.ID]; isLive {
			liveCount++
			continue
		}
		ops = append(ops, model.GarbageOp{
			Action:       model.GarbageActionDeleteTree,
			Target:       image.Key,
			Bytes:        utils.New(image.Bytes),
			Reason:       model.GarbageReasonUnreferenced,
			LastModified: image.LastModified,
		})
	}
	return model.OCIImageStat{
		GarbageFindings: model.GarbageFindings{Ops: ops},
		Live:            liveCount,
	}, nil
}

// Delete removes every image directory stat names, each one's manifest first.
func (s *Collector) Delete(
	ctx context.Context, scope gc.ComposeProjectScope, stat model.GarbageStat,
) (model.GarbageDeletion, error) {
	found, ok := stat.(model.OCIImageStat)
	if !ok {
		return model.GarbageDeletion{}, fmt.Errorf("%s cannot delete a %T", s.Kind(), stat)
	}
	return gc.DeleteOps(ctx, found.Ops, func(ctx context.Context, op model.GarbageOp) error {
		return imagecopy.DeleteStoredImage(ctx, scope.Store, op.Target)
	}), nil
}
