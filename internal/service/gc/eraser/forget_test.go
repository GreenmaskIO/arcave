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

package eraser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// ForgetEraser forgets each repository's snapshots in one run, one repository at a time in the order
// each first appears under a concurrency of 1; what the prune freed is reclaimed, the snapshots
// themselves free nothing.
func TestForgetEraser_Erase(t *testing.T) {
	var (
		snapshot      = strings.Repeat("a", 64)
		otherSnapshot = strings.Repeat("b", 64)
		cacheSnapshot = strings.Repeat("c", 64)
		dbOp          = model.GarbageOp{Action: model.GarbageActionForget, Target: cdc.NewSnapshotKey("volume-named-db", snapshot)}
		cacheOp       = model.GarbageOp{Action: model.GarbageActionForget, Target: cdc.NewSnapshotKey("volume-named-cache", cacheSnapshot)}
		otherDBOp     = model.GarbageOp{Action: model.GarbageActionForget, Target: cdc.NewSnapshotKey("volume-named-db", otherSnapshot)}
		pruneOp       = model.GarbageOp{Action: model.GarbageActionForget, Target: cdc.NewRepositoryKey("volume-named-logs")}
		ops           = []model.GarbageOp{dbOp, cacheOp, otherDBOp}
		zero          = utils.New(int64(0))
	)
	type forget struct {
		objectID string
		ids      []string
		freed    *int64
		err      error
		// cancels cancels the run once this forget is done.
		cancels bool
	}

	tests := []struct {
		name    string
		ops     []model.GarbageOp
		openErr error
		// forgets are the runs expected, in order.
		forgets   []forget
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "one run per repository",
			ops:  ops,
			forgets: []forget{
				{objectID: "volume-named-db", ids: []string{snapshot, otherSnapshot}, freed: utils.New(int64(7))},
				{objectID: "volume-named-cache", ids: []string{cacheSnapshot}, freed: utils.New(int64(3))},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{
					{Target: dbOp.Target, Bytes: zero}, {Target: otherDBOp.Target, Bytes: zero},
					{Target: cacheOp.Target, Bytes: zero},
				},
				Reclaimed: []model.GarbageReclaimed{
					{Target: "restic/volume-named-db", Bytes: utils.New(int64(7))},
					{Target: "restic/volume-named-cache", Bytes: utils.New(int64(3))},
				},
			},
		},
		{
			name: "a repository that fails fails its ops alone",
			ops:  ops,
			forgets: []forget{
				{objectID: "volume-named-db", ids: []string{snapshot, otherSnapshot}, err: errors.New("repository is locked")},
				{objectID: "volume-named-cache", ids: []string{cacheSnapshot}},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: cacheOp.Target, Bytes: zero}},
				Failed: []model.GarbageDeleteFailure{
					{Target: dbOp.Target, Error: "repository is locked"},
					{Target: otherDBOp.Target, Error: "repository is locked"},
				},
				Reclaimed: []model.GarbageReclaimed{{Target: "restic/volume-named-cache"}},
			},
		},
		{
			name:    "a repository's key prunes it alone",
			ops:     []model.GarbageOp{pruneOp},
			forgets: []forget{{objectID: "volume-named-logs", freed: utils.New(int64(5))}},
			want: model.GarbageDeletion{
				Deleted:   []model.GarbageDeleted{{Target: pruneOp.Target, Bytes: zero}},
				Reclaimed: []model.GarbageReclaimed{{Target: "restic/volume-named-logs", Bytes: utils.New(int64(5))}},
			},
		},
		{
			name: "a repository the stop cuts short fails interrupted",
			ops:  []model.GarbageOp{cacheOp},
			forgets: []forget{
				{objectID: "volume-named-cache", ids: []string{cacheSnapshot}, err: context.Canceled, cancels: true},
			},
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{
				{Target: cacheOp.Target, Error: "context canceled", Interrupted: true},
			}},
		},
		{
			name: "a target that is no snapshot key fails without a run",
			ops:  []model.GarbageOp{{Action: model.GarbageActionForget, Target: "restic/volume-named-db/locks/x"}},
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{{
				Target: "restic/volume-named-db/locks/x",
				Error:  `"restic/volume-named-db/locks/x" is no restic snapshot key`,
			}}},
		},
		{
			name: "a target that is no snapshot key fails beside the repositories that run",
			ops:  []model.GarbageOp{{Action: model.GarbageActionForget, Target: "restic/volume-named-db/locks/x"}, dbOp, cacheOp},
			forgets: []forget{
				{objectID: "volume-named-db", ids: []string{snapshot}, err: errors.New("repository is locked")},
				{objectID: "volume-named-cache", ids: []string{cacheSnapshot}, freed: utils.New(int64(3))},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: cacheOp.Target, Bytes: zero}},
				Failed: []model.GarbageDeleteFailure{
					{Target: "restic/volume-named-db/locks/x", Error: `"restic/volume-named-db/locks/x" is no restic snapshot key`},
					{Target: dbOp.Target, Error: "repository is locked"},
				},
				Reclaimed: []model.GarbageReclaimed{{Target: "restic/volume-named-cache", Bytes: utils.New(int64(3))}},
			},
		},
		{
			name: "a run cancelled between repositories leaves the rest not run",
			ops:  ops,
			forgets: []forget{
				{objectID: "volume-named-db", ids: []string{snapshot, otherSnapshot}, freed: utils.New(int64(7)), cancels: true},
			},
			want: model.GarbageDeletion{
				Deleted:   []model.GarbageDeleted{{Target: dbOp.Target, Bytes: zero}, {Target: otherDBOp.Target, Bytes: zero}},
				NotRun:    1,
				Reclaimed: []model.GarbageReclaimed{{Target: "restic/volume-named-db", Bytes: utils.New(int64(7))}},
			},
		},
		{
			name:    "repositories that cannot be opened fail every op",
			ops:     []model.GarbageOp{cacheOp},
			openErr: errors.New("port in use"),
			want: model.GarbageDeletion{Failed: []model.GarbageDeleteFailure{
				{Target: cacheOp.Target, Error: "open the restic repositories: port in use"},
			}},
		},
		{
			name:      "a cancelled run forgets nothing",
			ops:       ops,
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			store := &gctest.MockStore{}
			pruner := &mockPruner{}
			opener := &mockPrunerOpener{}
			if tt.openErr != nil {
				opener.On("Open", mock.Anything, store).Return(nil, tt.openErr).Once()
			} else {
				opener.On("Open", mock.Anything, store).Return(pruner, nil).Once()
				var calls []*mock.Call
				for _, forget := range tt.forgets {
					call := pruner.On("Forget", mock.Anything, forget.objectID, forget.ids).Return(forget.freed, forget.err).Once()
					if forget.cancels {
						call.Run(func(mock.Arguments) { cancel() })
					}
					calls = append(calls, call)
				}
				calls = append(calls, pruner.On("Close", mock.Anything).Once())
				mock.InOrder(calls...)
			}

			// Act
			got := NewForgetEraser(opener, 1).Erase(ctx, store, tt.ops)

			// Assert
			assert.Equal(t, tt.want, got)
			opener.AssertExpectations(t)
			pruner.AssertExpectations(t)
		})
	}
}

// ForgetEraser runs up to its concurrency of repositories at once: each forget here waits for the
// other to start.
func TestForgetEraser_Erase_concurrent(t *testing.T) {
	// Arrange
	var (
		dbSnapshot    = strings.Repeat("a", 64)
		cacheSnapshot = strings.Repeat("c", 64)
		ops           = []model.GarbageOp{
			{Action: model.GarbageActionForget, Target: cdc.NewSnapshotKey("volume-named-db", dbSnapshot)},
			{Action: model.GarbageActionForget, Target: cdc.NewSnapshotKey("volume-named-cache", cacheSnapshot)},
		}
		started sync.WaitGroup
	)
	started.Add(2)
	store := &gctest.MockStore{}
	pruner := &mockPruner{}
	opener := &mockPrunerOpener{}
	opener.On("Open", mock.Anything, store).Return(pruner, nil).Once()
	meet := func(mock.Arguments) {
		started.Done()
		started.Wait()
	}
	pruner.On("Forget", mock.Anything, "volume-named-db", []string{dbSnapshot}).Run(meet).Return(utils.New(int64(1)), nil).Once()
	pruner.On("Forget", mock.Anything, "volume-named-cache", []string{cacheSnapshot}).Run(meet).Return(utils.New(int64(2)), nil).Once()
	pruner.On("Close", mock.Anything).Once()

	// Act
	got := NewForgetEraser(opener, 2).Erase(context.Background(), store, ops)

	// Assert
	assert.Len(t, got.Deleted, 2)
	assert.Equal(t, []model.GarbageReclaimed{
		{Target: "restic/volume-named-db", Bytes: utils.New(int64(1))},
		{Target: "restic/volume-named-cache", Bytes: utils.New(int64(2))},
	}, got.Reclaimed, "assembled in the order the repositories first appear")
	opener.AssertExpectations(t)
	pruner.AssertExpectations(t)
}

// ForgetEraser reports one step per repository under the deletion's batch, ended with what its
// prune freed; a repository the run's end came before opens none.
func TestForgetEraser_Erase_progress(t *testing.T) {
	var (
		dbSnapshot = strings.Repeat("a", 64)
		ops        = []model.GarbageOp{
			{Action: model.GarbageActionForget, Target: cdc.NewSnapshotKey("volume-named-db", dbSnapshot)},
			{Action: model.GarbageActionForget, Target: cdc.NewRepositoryKey("volume-named-logs")},
		}
	)
	tests := []struct {
		name      string
		cancelled bool
		want      []string
	}{
		{
			name: "one step per repository",
			want: []string{
				"garbage[restic/volume-named-db] succeeded bytes=7",
				"garbage[restic/volume-named-logs] failed",
				"gc.delete[] failed items=1/1",
				"gc[] succeeded",
			},
		},
		{
			name:      "a run already stopped opens no repository",
			cancelled: true,
			want: []string{
				"gc.delete[] cancelled items=0/0",
				"gc[] succeeded",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var (
				recorder = &endedRecorder{steps: map[progress.StepID]progress.Step{}}
				tracker  = progress.New(recorder, func() time.Time { return time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC) })
			)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, root := tracker.Begin(ctx, progress.Step{Kind: "gc"})
			store := &gctest.MockStore{}
			pruner := &mockPruner{}
			opener := &mockPrunerOpener{}
			opener.On("Open", mock.Anything, store).Return(pruner, nil).Once()
			if tt.cancelled {
				cancel()
			} else {
				pruner.On("Forget", mock.Anything, "volume-named-db", []string{dbSnapshot}).
					Return(utils.New(int64(7)), nil).Once()
				pruner.On("Forget", mock.Anything, "volume-named-logs", []string(nil)).
					Return(nil, errors.New("repository is locked")).Once()
			}
			pruner.On("Close", mock.Anything).Once()

			// Act
			NewForgetEraser(opener, 1).Erase(ctx, store, ops)
			root.End(nil)

			// Assert
			assert.Equal(t, tt.want, recorder.ended)
			opener.AssertExpectations(t)
			pruner.AssertExpectations(t)
		})
	}
}

// endedRecorder renders every step's last event as "<kind>[<id>] <status> [bytes] [items]".
type endedRecorder struct {
	steps map[progress.StepID]progress.Step
	ended []string
}

func (r *endedRecorder) Render(evt progress.Event) {
	if evt.Open != nil {
		r.steps[evt.StepID] = evt.Open.Step
	}
	if !evt.Status.IsTerminal() {
		return
	}
	step := r.steps[evt.StepID]
	line := fmt.Sprintf("%s[%s] %s", step.Kind, step.ID, evt.Status)
	if evt.Sample.Bytes != nil {
		line += fmt.Sprintf(" bytes=%d", evt.Sample.Bytes.Done)
	}
	if evt.Sample.Items != nil {
		line += fmt.Sprintf(" items=%d/%d", evt.Sample.Items.Done, evt.Sample.Items.Failed)
	}
	r.ended = append(r.ended, line)
}
