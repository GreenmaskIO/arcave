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

// Package savepointdir is the savepoint-directory collector: the trees of dead captures.
package savepointdir

import (
	"context"
	"strings"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/utils"
)

// Collector finds the savepoint trees no record commits whose capture is dead:
// what a create or a remove left behind when it died between its data and its record.
type Collector struct{}

func New() *Collector { return &Collector{} }

func (s *Collector) Kind() model.GarbageCollectorKind {
	return model.GarbageCollectorKindSavepointDirectory
}

func (s *Collector) DataKind() model.GarbageDataKind {
	return model.GarbageDataKindSavepointData
}

// Find names every dead tree, holds back a live capture's and leaves unjudged one whose
// heartbeat cannot be read.
func (s *Collector) Find(_ context.Context, scope gc.ComposeProjectScope) (model.GarbageStat, error) {
	var (
		trees    = map[model.SavepointID][]storages.ObjectStat{}
		findings = gc.NewFindings()
	)
	for _, object := range scope.Objects {
		dir, _, _ := strings.Cut(object.Name, "/")
		trees[model.SavepointID(dir)] = append(trees[model.SavepointID(dir)], object)
	}

	for _, id := range scope.Dead {
		bytes, lastModified := utils.MeasureObjects(trees[id])
		findings.Ops = append(findings.Ops, model.GarbageOp{
			Action:       model.GarbageActionDeleteTree,
			Target:       id.String(),
			Bytes:        utils.New(bytes),
			Reason:       model.GarbageReasonDeadCapture,
			LastModified: lastModified,
			Savepoint:    id,
		})
	}
	for _, id := range scope.InProgress {
		_, lastModified := utils.MeasureObjects(trees[id])
		findings.Skipped = append(findings.Skipped, model.GarbageSkip{
			Target:       id.String(),
			Reason:       model.GarbageReasonCaptureInProgress,
			LastModified: lastModified,
		})
	}
	for _, unknown := range scope.Unknown {
		_, lastModified := utils.MeasureObjects(trees[unknown.ID])
		findings.Unjudged = append(findings.Unjudged, model.GarbageSkip{
			Target:       unknown.ID.String(),
			Reason:       model.GarbageReasonStatusUnknown,
			LastModified: lastModified,
			Error:        unknown.Error,
		})
	}
	return model.SavepointDirectoryStat{
		GarbageFindings: findings,
		Committed:       len(scope.Savepoints),
	}, nil
}
