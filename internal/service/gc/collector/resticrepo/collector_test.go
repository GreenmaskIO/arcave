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

package resticrepo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/utils/gctest"
	"github.com/greenmaskio/arcave/internal/utils"
)

// absentSavepoint is a savepoint the scope's listing did not hold: its tree is gone.
const absentSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000007")

// snapshotOf is a snapshot of id taken by savepoint's capture, at gctest.Old.
func snapshotOf(id string, savepoint model.SavepointID) cdc.Snapshot {
	return cdc.Snapshot{
		ID:        strings.Repeat(id, 64),
		Savepoint: savepoint,
		Time:      gctest.Old,
	}
}

// newRepoScope is the scope of a store holding the repositories named, with gctest.LiveSavepoint
// committed and gctest.DeadSavepoint's tree dead.
func newRepoScope(t *testing.T, repos ...string) gc.ComposeProjectScope {
	t.Helper()
	store, root := gctest.NewStore(t)
	for _, repo := range repos {
		gctest.WriteObject(t, root, "restic/"+repo+"/config", "c", gctest.Old)
	}
	scope := gctest.NewScope(t, store, model.Savepoint{ID: gctest.LiveSavepoint})
	scope.Dead = []model.SavepointID{gctest.DeadSavepoint}
	return scope
}

// Every snapshot is judged by its savepoint: one no record commits is dead, its tree listed or
// not; a repository restic cannot list leaves the others judged.
func TestCollector_Find(t *testing.T) {
	// Arrange
	ctx := context.Background()
	scope := newRepoScope(t, "volume-named-db", "volume-named-cache")
	snapshots := []cdc.Snapshot{
		snapshotOf("1", gctest.LiveSavepoint),
		snapshotOf("2", gctest.DeadSavepoint),
		snapshotOf("6", absentSavepoint),
		snapshotOf("7", ""),
	}
	inspector := &mockInspector{}
	opener := &mockOpener{}
	open := opener.On("Open", mock.Anything, scope.Store).Return(inspector, nil).Once()
	inspector.On("ReadSnapshots", mock.Anything, "volume-named-cache").
		Return(nil, errors.New("restic snapshots: wrong password")).Once().NotBefore(open)
	readDB := inspector.On("ReadSnapshots", mock.Anything, "volume-named-db").Return(snapshots, nil).Once()
	dead := []string{strings.Repeat("2", 64), strings.Repeat("6", 64)}
	estimate := inspector.On("EstimateForget", mock.Anything, "volume-named-db", dead).Return(utils.New(int64(4096)), nil).Once()
	closing := inspector.On("Close", mock.Anything).Return().Once()
	mock.InOrder(open, readDB, estimate, closing)

	// Act
	stat, err := New(&mockPinger{}, opener).Find(ctx, scope)

	// Assert
	require.NoError(t, err)
	got := stat.(model.ResticRepositoryStat)
	assert.Equal(t, []model.GarbageOp{
		{
			Action:       model.GarbageActionForget,
			Target:       "restic/volume-named-db/snapshots/" + dead[0],
			Reason:       model.GarbageReasonDeadCapture,
			LastModified: gctest.Old,
			Savepoint:    gctest.DeadSavepoint,
		},
		{
			Action:       model.GarbageActionForget,
			Target:       "restic/volume-named-db/snapshots/" + dead[1],
			Reason:       model.GarbageReasonDeadCapture,
			LastModified: gctest.Old,
			Savepoint:    absentSavepoint,
		},
	}, got.Ops)
	assert.Empty(t, got.Skipped)
	assert.Equal(t, []model.GarbageSkip{
		{
			Target: "restic/volume-named-cache",
			Reason: model.GarbageReasonBackendUnreadable,
			Error:  "restic snapshots: wrong password",
		},
	}, got.Unjudged)
	assert.Equal(t, 1, got.LiveRepos)
	assert.Equal(t, 1, got.LiveSnapshots)
	assert.Equal(t, 1, got.Unattributed, "a snapshot no capture tagged is not arcave's")
	assert.Equal(t, []model.ResticForgetEstimate{
		{
			Repo:       "restic/volume-named-db",
			Snapshots:  dead,
			FreedBytes: utils.New(int64(4096)),
		},
	}, got.Estimates)
	assert.Equal(t, utils.New(int64(4096)), got.Summary().Bytes)
	for _, op := range got.Ops {
		assert.Nil(t, op.Bytes, "a snapshot alone frees nothing a prune can name")
	}
	inspector.AssertExpectations(t)
	opener.AssertExpectations(t)
}

// An estimate restic cannot give leaves the ops judged and the bytes unknown.
func TestCollector_Find_Estimate(t *testing.T) {
	tests := []struct {
		name        string
		freed       *int64
		estimateErr error
	}{
		{
			name: "restic printed no estimate",
		},
		{
			name:        "the estimate failed",
			estimateErr: errors.New("restic forget: connection refused"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			scope := newRepoScope(t, "volume-named-db")
			inspector := &mockInspector{}
			inspector.On("ReadSnapshots", mock.Anything, "volume-named-db").
				Return([]cdc.Snapshot{snapshotOf("2", gctest.DeadSavepoint)}, nil).Once()
			inspector.On("EstimateForget", mock.Anything, "volume-named-db", []string{strings.Repeat("2", 64)}).
				Return(tt.freed, tt.estimateErr).Once()
			inspector.On("Close", mock.Anything).Return().Once()
			opener := &mockOpener{}
			opener.On("Open", mock.Anything, scope.Store).Return(inspector, nil).Once()

			// Act
			stat, err := New(&mockPinger{}, opener).Find(context.Background(), scope)

			// Assert
			require.NoError(t, err)
			got := stat.(model.ResticRepositoryStat)
			assert.Len(t, got.Ops, 1)
			assert.Empty(t, got.Unjudged)
			require.Len(t, got.Estimates, 1)
			assert.Nil(t, got.Estimates[0].FreedBytes)
			assert.Nil(t, got.Summary().Bytes)
			inspector.AssertExpectations(t)
			opener.AssertExpectations(t)
		})
	}
}

// A repository with no dead snapshot is named for a prune alone only while a prune would free
// something in it: a prune that failed after its forget is taken up again.
func TestCollector_Find_Unpruned(t *testing.T) {
	tests := []struct {
		name        string
		freed       *int64
		estimateErr error
		wantOps     []model.GarbageOp
		want        []model.ResticForgetEstimate
	}{
		{
			name:  "data a prune would free",
			freed: utils.New(int64(4096)),
			wantOps: []model.GarbageOp{{
				Action: model.GarbageActionForget,
				Target: "restic/volume-named-db",
				Reason: model.GarbageReasonUnprunedData,
			}},
			want: []model.ResticForgetEstimate{{
				Repo:       "restic/volume-named-db",
				Snapshots:  []string{},
				FreedBytes: utils.New(int64(4096)),
			}},
		},
		{name: "nothing a prune would free", freed: utils.New(int64(0))},
		{name: "restic printed no estimate"},
		{name: "the estimate failed", estimateErr: errors.New("restic prune: connection refused")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			scope := newRepoScope(t, "volume-named-db")
			inspector := &mockInspector{}
			opener := &mockOpener{}
			mock.InOrder(
				opener.On("Open", mock.Anything, scope.Store).Return(inspector, nil).Once(),
				inspector.On("ReadSnapshots", mock.Anything, "volume-named-db").
					Return([]cdc.Snapshot{snapshotOf("1", gctest.LiveSavepoint)}, nil).Once(),
				inspector.On("EstimateForget", mock.Anything, "volume-named-db", []string(nil)).
					Return(tt.freed, tt.estimateErr).Once(),
				inspector.On("Close", mock.Anything).Return().Once(),
			)

			// Act
			stat, err := New(&mockPinger{}, opener).Find(context.Background(), scope)

			// Assert
			require.NoError(t, err)
			got := stat.(model.ResticRepositoryStat)
			assert.Equal(t, append([]model.GarbageOp{}, tt.wantOps...), got.Ops)
			assert.Equal(t, tt.want, got.Estimates)
			assert.Equal(t, 1, got.LiveSnapshots)
			inspector.AssertExpectations(t)
			opener.AssertExpectations(t)
		})
	}
}

// A compose project without repositories starts no restic server.
func TestCollector_Find_NoRepositories(t *testing.T) {
	// Arrange
	scope := newRepoScope(t)
	opener := &mockOpener{}

	// Act
	stat, err := New(&mockPinger{}, opener).Find(context.Background(), scope)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ResticRepositoryStat{GarbageFindings: model.GarbageFindings{Ops: []model.GarbageOp{}}}, stat)
	assert.Equal(t, utils.New(int64(0)), stat.Summary().Bytes)
	opener.AssertExpectations(t)
}

// The repositories cannot be read at all when the server does not start.
func TestCollector_Find_OpenFails(t *testing.T) {
	// Arrange
	scope := newRepoScope(t, "volume-named-db")
	opener := &mockOpener{}
	opener.On("Open", mock.Anything, scope.Store).Return(nil, errors.New("address already in use")).Once()

	// Act
	_, err := New(&mockPinger{}, opener).Find(context.Background(), scope)

	// Assert
	require.ErrorContains(t, err, "address already in use")
	opener.AssertExpectations(t)
}

func TestCollector_Probe(t *testing.T) {
	tests := []struct {
		name    string
		pingErr error
	}{
		{name: "the daemon answers"},
		{name: "the daemon does not", pingErr: errors.New("connection refused")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			daemon := &mockPinger{}
			daemon.On("Ping", mock.Anything).Return(tt.pingErr).Once()

			// Act
			err := New(daemon, &mockOpener{}).Probe(context.Background())

			// Assert
			assert.Equal(t, tt.pingErr, err)
			daemon.AssertExpectations(t)
		})
	}
}
