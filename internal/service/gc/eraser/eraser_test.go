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

	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// TreeEraser removes each tree its ops name, whole, in order.
func TestTreeEraser_Erase(t *testing.T) {
	var (
		dead      = gctest.DeadSavepoint.String()
		otherDead = gctest.OtherDeadSavepoint.String()
		ops       = []model.GarbageOp{
			{Action: model.GarbageActionDeleteTree, Target: dead, Bytes: utils.New(int64(5))},
			{Action: model.GarbageActionDeleteTree, Target: otherDead, Bytes: utils.New(int64(2))},
		}
		bothDeleted = []model.GarbageDeleted{
			{Target: dead, Bytes: utils.New(int64(5))}, {Target: otherDead, Bytes: utils.New(int64(2))},
		}
	)

	tests := []struct {
		name string
		// errs is what deleting each tree returns, one per tree in order; nil expects no deletion.
		errs      []error
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "every dead tree goes",
			errs: []error{nil, nil},
			want: model.GarbageDeletion{Deleted: bothDeleted},
		},
		{
			name: "a tree already gone counts as deleted, freeing nothing",
			errs: []error{&storages.MissingObjectsError{Paths: []string{dead}}, nil},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: dead, Bytes: utils.New(int64(0))}, {Target: otherDead, Bytes: utils.New(int64(2))},
			}},
		},
		{
			name: "a tree that cannot go fails alone",
			errs: []error{errors.New("access denied"), nil},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: otherDead, Bytes: utils.New(int64(2))}},
				Failed:  []model.GarbageDeleteFailure{{Target: dead, Error: "access denied"}},
			},
		},
		{
			name:      "a cancelled run deletes nothing",
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			var calls []*mock.Call
			for i, err := range tt.errs {
				calls = append(calls, store.On("DeleteAll", mock.Anything, ops[i].Target).Return(err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := NewTreeEraser().Erase(ctx, store, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			store.AssertExpectations(t)
		})
	}
}

// ImageEraser removes each stored image its ops name, its manifest first: a removal cut short
// must leave no manifest a capture would take for the image stored.
func TestImageEraser_Erase(t *testing.T) {
	var (
		image2 = "shared/oci/images/sha256/" + strings.Repeat("2", 64)
		image3 = "shared/oci/images/sha256/" + strings.Repeat("3", 64)
		ops    = []model.GarbageOp{
			{Action: model.GarbageActionDeleteImage, Target: image2, Bytes: utils.New(int64(4))},
			{Action: model.GarbageActionDeleteImage, Target: image3, Bytes: utils.New(int64(2))},
		}
		missing  = &storages.MissingObjectsError{Paths: []string{image3 + "/image.json"}}
		denied   = errors.New("access denied")
		image3Ok = model.GarbageDeleted{Target: image3, Bytes: utils.New(int64(2))}
	)

	// step is one deletion the store expects, in order.
	type step struct {
		method string
		arg    any
		err    error
	}
	tests := []struct {
		name      string
		steps     []step
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "the manifest goes before the directory, and one without a manifest goes whole",
			steps: []step{
				{"Delete", []string{image2 + "/image.json"}, nil}, {"DeleteAll", image2, nil},
				{"Delete", []string{image3 + "/image.json"}, missing}, {"DeleteAll", image3, nil},
			},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: image2, Bytes: utils.New(int64(4))}, image3Ok,
			}},
		},
		{
			name: "a manifest that cannot go keeps its directory",
			steps: []step{
				{"Delete", []string{image2 + "/image.json"}, denied},
				{"Delete", []string{image3 + "/image.json"}, nil}, {"DeleteAll", image3, nil},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{image3Ok},
				Failed:  []model.GarbageDeleteFailure{{Target: image2, Error: "delete " + image2 + "/image.json: access denied"}},
			},
		},
		{
			name: "a directory cut short past its manifest fails, its manifest gone",
			steps: []step{
				{"Delete", []string{image2 + "/image.json"}, nil}, {"DeleteAll", image2, denied},
				{"Delete", []string{image3 + "/image.json"}, nil}, {"DeleteAll", image3, nil},
			},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{image3Ok},
				Failed:  []model.GarbageDeleteFailure{{Target: image2, Error: "delete " + image2 + ": access denied"}},
			},
		},
		{
			name:      "a cancelled run deletes nothing",
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			var calls []*mock.Call
			for _, step := range tt.steps {
				calls = append(calls, store.On(step.method, mock.Anything, step.arg).Return(step.err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := NewImageEraser().Erase(ctx, store, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			store.AssertExpectations(t)
		})
	}
}

// ObjectEraser removes each blob its ops name, one at a time, in order.
func TestObjectEraser_Erase(t *testing.T) {
	var (
		dead    = "shared/oci/blobs/sha256/" + gctest.DeadBlob
		newDead = "shared/oci/blobs/sha256/" + gctest.NewBlob
		ops     = []model.GarbageOp{
			{Action: model.GarbageActionDeleteObject, Target: dead, Bytes: utils.New(int64(1))},
			{Action: model.GarbageActionDeleteObject, Target: newDead, Bytes: utils.New(int64(1))},
		}
		bothDeleted = []model.GarbageDeleted{
			{Target: dead, Bytes: utils.New(int64(1))}, {Target: newDead, Bytes: utils.New(int64(1))},
		}
	)

	tests := []struct {
		name string
		// errs is what deleting each blob returns, one per blob in order; nil expects no deletion.
		errs      []error
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "every dead blob goes",
			errs: []error{nil, nil},
			want: model.GarbageDeletion{Deleted: bothDeleted},
		},
		{
			name: "a blob already gone counts as deleted, freeing nothing",
			errs: []error{&storages.MissingObjectsError{Paths: []string{dead}}, nil},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: dead, Bytes: utils.New(int64(0))}, {Target: newDead, Bytes: utils.New(int64(1))},
			}},
		},
		{
			name: "a blob that cannot go fails alone",
			errs: []error{errors.New("access denied"), nil},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: newDead, Bytes: utils.New(int64(1))}},
				Failed:  []model.GarbageDeleteFailure{{Target: dead, Error: "access denied"}},
			},
		},
		{
			name:      "a cancelled run deletes nothing",
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			var calls []*mock.Call
			for i, err := range tt.errs {
				calls = append(calls, store.On("Delete", mock.Anything, []string{ops[i].Target}).Return(err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := NewObjectEraser().Erase(ctx, store, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			store.AssertExpectations(t)
		})
	}
}

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

// TagEraser takes each tag off the daemon unforced; one a container uses fails alone.
func TestTagEraser_Erase(t *testing.T) {
	const (
		ref      = "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000000"
		otherRef = "arcave/demo-db-1-postgres-16:sp-019e843e-1650-7000-8000-000000000001"
	)
	ops := []model.GarbageOp{
		{Action: model.GarbageActionUntag, Target: ref},
		{Action: model.GarbageActionUntag, Target: otherRef},
	}

	tests := []struct {
		name string
		// errs is what removing each tag returns, one per op in order.
		errs      []error
		cancelled bool
		want      model.GarbageDeletion
	}{
		{
			name: "every tag goes",
			errs: []error{nil, nil},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{{Target: ref}, {Target: otherRef}}},
		},
		{
			name: "a tag a container uses fails alone",
			errs: []error{errors.New("image is being used by stopped container"), nil},
			want: model.GarbageDeletion{
				Deleted: []model.GarbageDeleted{{Target: otherRef}},
				Failed:  []model.GarbageDeleteFailure{{Target: ref, Error: "image is being used by stopped container"}},
			},
		},
		{
			name: "a tag already gone frees nothing",
			errs: []error{fmt.Errorf("remove image reference: %w", model.ErrImageRefNotFound), nil},
			want: model.GarbageDeletion{Deleted: []model.GarbageDeleted{
				{Target: ref, Bytes: utils.New(int64(0))}, {Target: otherRef},
			}},
		},
		{
			name:      "a cancelled run untags nothing",
			cancelled: true,
			want:      model.GarbageDeletion{NotRun: 2},
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
			daemon := &mockTagRemover{}
			var calls []*mock.Call
			for i, err := range tt.errs {
				calls = append(calls, daemon.On("RemoveImageRef", mock.Anything, model.ImageRef(ops[i].Target)).Return(err).Once())
			}
			if len(calls) > 1 {
				mock.InOrder(calls...)
			}

			// Act
			got := NewTagEraser(daemon).Erase(ctx, ops)

			// Assert
			assert.Equal(t, tt.want, got)
			daemon.AssertExpectations(t)
		})
	}
}
