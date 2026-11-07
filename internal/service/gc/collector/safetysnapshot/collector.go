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

// Package safetysnapshot is the safety-snapshot-retention collector: the committed safety snapshots
// beyond the newest the config retains.
package safetysnapshot

import (
	"cmp"
	"context"
	"slices"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/utils"
)

// Collector finds a compose project's safety snapshots older than its retainRecent newest.
type Collector struct {
	retainRecent int
}

func New(retainRecent int) *Collector {
	return &Collector{retainRecent: retainRecent}
}

func (s *Collector) Kind() model.GarbageCollectorKind {
	return model.GarbageCollectorKindSafetySnapshotRetention
}

func (s *Collector) DataKind() model.GarbageDataKind {
	return model.GarbageDataKindSafetySnapshots
}

// Find names every safety snapshot past the retention, newest first. Age is the id's, a UUIDv7,
// not CreatedAt's, another host's clock.
func (s *Collector) Find(_ context.Context, scope gc.ComposeProjectScope) (model.GarbageStat, error) {
	var (
		filesBySavepoint = gc.GroupFilesBySavepoint(scope.Objects)
		ids              []model.SavepointID
		ops              = []model.GarbageOp{}
		retained         int
	)
	for _, savepoint := range scope.Savepoints {
		if savepoint.IsSafetySnapshot() {
			ids = append(ids, savepoint.ID)
		}
	}
	slices.SortFunc(ids, func(a, b model.SavepointID) int { return cmp.Compare(b, a) })

	retained = min(s.retainRecent, len(ids))
	for _, id := range ids[retained:] {
		bytes, lastModified := utils.MeasureObjects(filesBySavepoint[id])
		ops = append(ops, model.GarbageOp{
			Action:       model.GarbageActionDeleteSavepoint,
			Target:       id.String(),
			Bytes:        utils.New(bytes),
			Reason:       model.GarbageReasonBeyondRetention,
			LastModified: lastModified,
		})
	}
	return model.SafetySnapshotStat{
		GarbageFindings: model.GarbageFindings{Ops: ops},
		Retained:        retained,
	}, nil
}
