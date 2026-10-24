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

const (
	// absentAlive, absentDead and absentUnknown are savepoints the scope's listing did not hold:
	// one whose capture has started since, one that never had a tree, one whose status cannot be read.
	absentAlive   = model.SavepointID("019e843e-1650-7000-8000-000000000006")
	absentDead    = model.SavepointID("019e843e-1650-7000-8000-000000000007")
	absentUnknown = model.SavepointID("019e843e-1650-7000-8000-000000000008")
)

// snapshotOf is a snapshot of id taken by savepoint's capture, at gctest.Old.
func snapshotOf(id string, savepoint model.SavepointID) cdc.Snapshot {
	return cdc.Snapshot{
		ID:        strings.Repeat(id, 64),
		Savepoint: savepoint,
		Time:      gctest.Old,
	}
}

// newRepoScope is the scope of a store holding the repositories named, with gctest.LiveSavepoint
// committed and captures answering for the savepoints its listing did not hold.
func newRepoScope(t *testing.T, captures *mockCaptures, repos ...string) gc.ComposeProjectScope {
	t.Helper()
	store, root := gctest.NewStore(t)
	for _, repo := range repos {
		gctest.WriteObject(t, root, "restic/"+repo+"/config", "c", gctest.Old)
	}
	scope := gctest.CaptureStatuses{
		InProgress: []model.SavepointID{gctest.FreshSavepoint},
		Dead:       []model.SavepointID{gctest.DeadSavepoint},
		Unknown:    []gc.UnknownCapture{{ID: gctest.UnknownSavepoint, Error: "decode heartbeat: torn"}},
	}.Apply(gctest.NewScope(t, store, model.Savepoint{ID: gctest.LiveSavepoint}))
	scope.ReadCaptureStatus = captures.ReadCaptureStatus
	return scope
}

// Every snapshot is judged by its savepoint; a savepoint the listing did not hold is read now,
// and a repository restic cannot list leaves the others judged.
func TestCollector_Find(t *testing.T) {
	// Arrange
	ctx := context.Background()
	captures := &mockCaptures{}
	captures.On("ReadCaptureStatus", mock.Anything, absentAlive).Return(true, nil).Once()
	captures.On("ReadCaptureStatus", mock.Anything, absentDead).Return(false, nil).Once()
	captures.On("ReadCaptureStatus", mock.Anything, absentUnknown).Return(false, errors.New("stat savepoint record: timeout")).Once()
	scope := newRepoScope(t, captures, "volume-named-db", "volume-named-cache")
	snapshots := []cdc.Snapshot{
		snapshotOf("1", gctest.LiveSavepoint),
		snapshotOf("2", gctest.DeadSavepoint),
		snapshotOf("3", gctest.FreshSavepoint),
		snapshotOf("4", gctest.UnknownSavepoint),
		snapshotOf("5", absentAlive),
		snapshotOf("6", absentDead),
		snapshotOf("7", ""),
		snapshotOf("8", absentUnknown),
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
			Savepoint:    absentDead,
		},
	}, got.Ops)
	assert.Equal(t, []string{
		"restic/volume-named-db/snapshots/" + strings.Repeat("3", 64),
		"restic/volume-named-db/snapshots/" + strings.Repeat("5", 64),
	}, gctest.SkippedTargets(got.Skipped))
	assert.Equal(t, []model.GarbageReason{
		model.GarbageReasonCaptureInProgress, model.GarbageReasonCaptureInProgress,
	}, gctest.Reasons(got.Skipped))
	assert.Equal(t, []model.GarbageSkip{
		{
			Target: "restic/volume-named-cache",
			Reason: model.GarbageReasonBackendUnreadable,
			Error:  "restic snapshots: wrong password",
		},
		{
			Target:       "restic/volume-named-db/snapshots/" + strings.Repeat("4", 64),
			Reason:       model.GarbageReasonStatusUnknown,
			LastModified: gctest.Old,
			Error:        "decode heartbeat: torn",
		},
		{
			Target:       "restic/volume-named-db/snapshots/" + strings.Repeat("8", 64),
			Reason:       model.GarbageReasonStatusUnknown,
			LastModified: gctest.Old,
			Error:        "stat savepoint record: timeout",
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
	captures.AssertExpectations(t)
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
			scope := newRepoScope(t, &mockCaptures{}, "volume-named-db")
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

// A compose project without repositories starts no restic server.
func TestCollector_Find_NoRepositories(t *testing.T) {
	// Arrange
	scope := newRepoScope(t, &mockCaptures{})
	opener := &mockOpener{}

	// Act
	stat, err := New(&mockPinger{}, opener).Find(context.Background(), scope)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, model.ResticRepositoryStat{GarbageFindings: gc.NewFindings()}, stat)
	assert.Equal(t, utils.New(int64(0)), stat.Summary().Bytes)
	opener.AssertExpectations(t)
}

// The repositories cannot be read at all when the server does not start.
func TestCollector_Find_OpenFails(t *testing.T) {
	// Arrange
	scope := newRepoScope(t, &mockCaptures{}, "volume-named-db")
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
