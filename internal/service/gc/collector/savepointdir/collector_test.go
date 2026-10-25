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

package savepointdir

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// Every dead tree goes, however young.
func TestCollector_Find(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := gctest.NewStore(t)
	gctest.WriteObject(t, root, gctest.LiveSavepoint.String()+"/savepoint.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, gctest.DeadSavepoint.String()+"/volume-named-db/data.tar", "12345", gctest.Recent)
	gctest.WriteObject(t, root, gctest.OtherDeadSavepoint.String()+"/heartbeat.json", "{}", gctest.Recent)
	gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "x", gctest.Old)
	scope := gctest.NewScope(t, store, model.Savepoint{ID: gctest.LiveSavepoint})
	scope.Dead = []model.SavepointID{gctest.DeadSavepoint, gctest.OtherDeadSavepoint}

	// Act
	stat, err := New().Find(ctx, scope)

	// Assert
	require.NoError(t, err)
	got := stat.(model.SavepointDirectoryStat)
	// The directory store reports local times.
	for i := range got.Ops {
		got.Ops[i].LastModified = got.Ops[i].LastModified.UTC()
	}
	assert.Equal(t, []model.GarbageOp{
		{
			Action: model.GarbageActionDeleteTree, Target: gctest.DeadSavepoint.String(), Bytes: utils.New(int64(5)),
			Reason: model.GarbageReasonDeadCapture, LastModified: gctest.Recent, Savepoint: gctest.DeadSavepoint,
		},
		{
			Action: model.GarbageActionDeleteTree, Target: gctest.OtherDeadSavepoint.String(), Bytes: utils.New(int64(2)),
			Reason: model.GarbageReasonDeadCapture, LastModified: gctest.Recent, Savepoint: gctest.OtherDeadSavepoint,
		},
	}, got.Ops)
	assert.Empty(t, got.Skipped)
	assert.Empty(t, got.Unjudged)
	assert.Equal(t, 1, got.Committed)
}
