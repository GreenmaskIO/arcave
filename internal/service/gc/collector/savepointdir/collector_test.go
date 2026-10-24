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
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// A tree goes by its capture's status, never by its age: dead goes whatever it was written,
// a live capture's is held back, an unreadable heartbeat's unjudged.
func TestCollector_Find(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store, root := gctest.NewStore(t)
	gctest.WriteObject(t, root, gctest.LiveSavepoint.String()+"/savepoint.json", "{}", gctest.Old)
	gctest.WriteObject(t, root, gctest.DeadSavepoint.String()+"/volume-named-db/data.tar", "12345", gctest.Recent)
	gctest.WriteObject(t, root, gctest.FreshSavepoint.String()+"/volume-named-db/data.tar", "1", gctest.Recent)
	gctest.WriteObject(t, root, gctest.UnknownSavepoint.String()+"/heartbeat.json", "{torn", gctest.Recent)
	gctest.WriteObject(t, root, "shared/oci/blobs/sha256/"+gctest.DeadBlob, "x", gctest.Old)
	statuses := gctest.CaptureStatuses{
		InProgress: []model.SavepointID{gctest.FreshSavepoint},
		Dead:       []model.SavepointID{gctest.DeadSavepoint},
		Unknown:    []gc.UnknownCapture{{ID: gctest.UnknownSavepoint, Error: "decode heartbeat"}},
	}

	// Act
	stat, err := New().Find(ctx, statuses.Apply(gctest.NewScope(t, store, model.Savepoint{ID: gctest.LiveSavepoint})))

	// Assert
	require.NoError(t, err)
	got := stat.(model.SavepointDirectoryStat)
	// The directory store reports local times.
	for i := range got.Ops {
		got.Ops[i].LastModified = got.Ops[i].LastModified.UTC()
	}
	for _, skips := range [][]model.GarbageSkip{got.Skipped, got.Unjudged} {
		for i := range skips {
			skips[i].LastModified = skips[i].LastModified.UTC()
		}
	}
	assert.Equal(t, []model.GarbageOp{{
		Action: model.GarbageActionDeleteTree, Target: gctest.DeadSavepoint.String(), Bytes: utils.New(int64(5)),
		Reason: model.GarbageReasonDeadCapture, LastModified: gctest.Recent, Savepoint: gctest.DeadSavepoint,
	}}, got.Ops)
	assert.Equal(t, []model.GarbageSkip{
		{Target: gctest.FreshSavepoint.String(), Reason: model.GarbageReasonCaptureInProgress, LastModified: gctest.Recent},
	}, got.Skipped)
	assert.Equal(t, []model.GarbageSkip{
		{Target: gctest.UnknownSavepoint.String(), Reason: model.GarbageReasonStatusUnknown, LastModified: gctest.Recent,
			Error: "decode heartbeat"},
	}, got.Unjudged)
	assert.Equal(t, 1, got.Committed)
}
