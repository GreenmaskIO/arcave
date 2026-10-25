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

// Package ociblob is the oci-blobs collector: deduplicated blobs no manifest lists.
package ociblob

import (
	"context"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/utils"
)

// Collector finds deduplicated blobs no manifest in the store lists.
type Collector struct {
	concurrency int
}

// New reads at most concurrency manifests at once.
func New(concurrency int) *Collector {
	return &Collector{concurrency: concurrency}
}

func (s *Collector) Kind() model.GarbageCollectorKind {
	return model.GarbageCollectorKindOCIBlobs
}

func (s *Collector) DataKind() model.GarbageDataKind { return model.GarbageDataKindImageLayers }

// Find names every blob no live manifest lists. One an unreferenced manifest lists is held back,
// and freed by the run after the one that removes that manifest: a capture reuses an intact
// manifest's blobs without writing them. An unreadable manifest aborts the kind.
func (s *Collector) Find(ctx context.Context, scope gc.ComposeProjectScope) (model.GarbageStat, error) {
	var (
		blobs   = dedupcopy.NewStoredBlobs(scope.Objects)
		ops     = []model.GarbageOp{}
		skipped []model.GarbageSkip
		live    int
	)
	references, err := readBlobReferences(ctx, scope, s.concurrency)
	if err != nil {
		return nil, err
	}
	if len(references.unreadable) > 0 {
		return model.OCIBlobStat{
			GarbageFindings: model.GarbageFindings{
				Ops:     ops,
				Aborted: fmt.Sprintf("%d image manifests cannot be read", len(references.unreadable)),
			},
			Unreadable: references.unreadable,
		}, nil
	}

	for _, blob := range blobs {
		switch {
		case references.bySavepoints[blob.Digest]:
			live++
		case references.byUnnamed[blob.Digest]:
			skipped = append(skipped, model.GarbageSkip{
				Target:       blob.Key,
				Reason:       model.GarbageReasonListedByUnreferenced,
				LastModified: blob.LastModified,
			})
		default:
			ops = append(ops, model.GarbageOp{
				Action:       model.GarbageActionDeleteObject,
				Target:       blob.Key,
				Bytes:        utils.New(blob.Bytes),
				Reason:       model.GarbageReasonUnreferenced,
				LastModified: blob.LastModified,
				Savepoint:    newSoleDeadSavepoint(references.byDeadSavepoints[blob.Digest]),
			})
		}
	}
	return model.OCIBlobStat{
		GarbageFindings: model.GarbageFindings{
			Ops:     ops,
			Skipped: skipped,
		},
		Live: live,
	}, nil
}

// newSoleDeadSavepoint is the dead savepoint a blob's removal is attributed to: the one tree of
// trees, or none when several list it.
func newSoleDeadSavepoint(trees []model.SavepointID) model.SavepointID {
	if len(trees) != 1 {
		return ""
	}
	return trees[0]
}
