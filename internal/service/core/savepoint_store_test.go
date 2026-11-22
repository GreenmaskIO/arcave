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

package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

// A safety snapshot is an ordinary record in the one store, so what keeps it out
// of a listing is the flag and nothing structural.
func TestService_ListSavepoints(t *testing.T) {
	tests := []struct {
		name string
		req  model.ListRequest
		want []model.Savepoint
	}{
		{name: "hidden by default", req: model.ListRequest{}, want: []model.Savepoint{demoRecord()}},
		{
			name: "shown with the flag, newest first by default",
			req:  model.ListRequest{IncludeSafetySnapshots: true},
			want: []model.Savepoint{demoSafetySnapshot(), demoRecord()},
		},
		{
			name: "desc",
			req:  model.ListRequest{IncludeSafetySnapshots: true, Sort: model.SortDesc},
			want: []model.Savepoint{demoSafetySnapshot(), demoRecord()},
		},
		{
			name: "asc keeps the repository's order",
			req:  model.ListRequest{IncludeSafetySnapshots: true, Sort: model.SortAsc},
			want: []model.Savepoint{demoRecord(), demoSafetySnapshot()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepoints{}
			repo.On("List", mock.Anything).Return([]model.Savepoint{demoRecord(), demoSafetySnapshot()}, nil).Once()
			// No daemon collaborator at all: savepoint list runs where no daemon does.
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				SessionLocker: fakeSessionLocker{},
				Savepoints:    repo, Inflight: &fakeInflight{},
			})

			// Act
			got, err := svc.ListSavepoints(zerolog.Nop().WithContext(context.Background()), tt.req)

			// Assert
			// No lock is taken and no backend built: neither collaborator is even
			// supplied, so reaching for one would panic.
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			repo.AssertExpectations(t)
		})
	}
}

// Removal deletes the savepoint it selects through the repository, which owns the order.
func TestService_RemoveSavepoints(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepoints{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("Remove", mock.Anything, record.ID).Return(nil).Once()

	// No daemon collaborator at all: savepoint remove runs where no daemon does.
	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		Savepoints:     repo, ProjectLocker: locker,
		Inflight: &fakeInflight{},
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A scanning selector never sees a safety snapshot, so retention neither counts one
// among the newest it keeps nor deletes one it never meant. An explicit id does
// see it, and that is how one is removed by hand.
func TestService_RemoveSavepoints_SafetySnapshotVisibility(t *testing.T) {
	record, backup := demoRecord(), demoSafetySnapshot()

	tests := []struct {
		name string
		req  model.RemoveRequest
		want []model.Savepoint
	}{
		{
			// The backup is the newest of the two, so counting it would retain it and
			// delete the savepoint instead — exactly backwards.
			name: "retain-recent 1 keeps the savepoint and leaves the backup alone",
			req:  model.RemoveRequest{RetainRecent: utils.New(1), DryRun: true},
			want: nil,
		},
		{
			// Deleting every savepoint still leaves the undo: the scan never sees it.
			name: "retain-recent 0 takes the savepoint and leaves the backup alone",
			req:  model.RemoveRequest{RetainRecent: utils.New(0), DryRun: true},
			want: []model.Savepoint{record},
		},
		{
			name: "an age rule sweeps neither",
			req:  model.RemoveRequest{BeforeDate: "2026-06-12T11:30:00Z", DryRun: true},
			want: []model.Savepoint{record},
		},
		{
			name: "an explicit id reaches the backup",
			req:  model.RemoveRequest{Refs: []string{backup.ID.String()}, DryRun: true},
			want: []model.Savepoint{backup},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := &mockSavepoints{}
			repo.On("List", mock.Anything).Return([]model.Savepoint{record, backup}, nil).Once()
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				SessionLocker: fakeSessionLocker{},
				Savepoints:    repo, ProjectLocker: nil,
				Inflight: &fakeInflight{},
			})

			// Act
			res, err := svc.RemoveSavepoints(zerolog.Nop().WithContext(context.Background()), tt.req)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.Removed)
			repo.AssertExpectations(t)
		})
	}
}

// A savepoint the repository fails to delete may be left uncommitted with its tree behind it; the
// orphan is named in the error.
func TestService_RemoveSavepoints_DeleteFailureNamesTheOrphan(t *testing.T) {
	// Arrange
	record := demoRecord()
	locker := &mockLocker{}
	expectLock(locker)
	repo := &mockSavepoints{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	repo.On("Remove", mock.Anything, record.ID).Return(errors.New("remove boom")).Once()

	svc := New(Deps{
		ArcaveProject:  demoArcaveProject,
		SessionLocker:  fakeSessionLocker{},
		WriteAdmission: fakeWriteAdmission{},
		Savepoints:     repo, ProjectLocker: locker,
		Inflight: &fakeInflight{},
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}})

	// Assert
	require.EqualError(t, err, "remove savepoint "+record.ID.String()+": remove boom; if its record went, the directory "+
		"is orphaned for a gc; the savepoints not yet removed are kept, a partly removed one is left as an orphan directory")
	locker.AssertExpectations(t)
	repo.AssertExpectations(t)
}

// A dry run takes no lock and deletes nothing.
func TestService_RemoveSavepoints_DryRun(t *testing.T) {
	// Arrange
	record := model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "sp", CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC)}
	repo := &mockSavepoints{}
	repo.On("List", mock.Anything).Return([]model.Savepoint{record}, nil)
	locker := &mockLocker{}

	svc := New(Deps{
		ArcaveProject: demoArcaveProject,
		SessionLocker: fakeSessionLocker{},
		Savepoints:    repo, ProjectLocker: locker,
		Inflight: &fakeInflight{},
	})
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{record.ID.String()}, DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	// Only List is expected: a Delete or an Acquire would fail the run where it happened.
	assert.Equal(t, []model.Savepoint{record}, res.Removed)
	repo.AssertExpectations(t)
	locker.AssertExpectations(t)
}

// TestValidateRemoveRequest covers the selector rules. A negative --retain-recent
// must be reported as such, not as a missing selector.
func TestValidateRemoveRequest(t *testing.T) {
	tests := []struct {
		name string
		req  model.RemoveRequest
		want error
	}{
		{name: "explicit ids", req: model.RemoveRequest{Refs: []string{"1"}}},
		{name: "retain recent", req: model.RemoveRequest{RetainRecent: utils.New(2)}},
		{name: "retain recent zero is a rule", req: model.RemoveRequest{RetainRecent: utils.New(0)}},
		{name: "retain for", req: model.RemoveRequest{RetainFor: "1d"}},
		{
			name: "ids and retention conflict",
			req:  model.RemoveRequest{Refs: []string{"1"}, RetainRecent: utils.New(2)},
			want: model.ErrRemoveSelectorConflict,
		},
		{
			name: "ids and retain recent zero conflict",
			req:  model.RemoveRequest{Refs: []string{"1"}, RetainRecent: utils.New(0)},
			want: model.ErrRemoveSelectorConflict,
		},
		{name: "no selector at all", req: model.RemoveRequest{}, want: model.ErrRemoveNoSelector},
		{
			name: "negative retain recent",
			req:  model.RemoveRequest{RetainRecent: utils.New(-1)},
			want: model.ErrRetainRecentNegative,
		},
		{
			name: "negative retain recent beats a conflicting id list",
			req:  model.RemoveRequest{Refs: []string{"1"}, RetainRecent: utils.New(-1)},
			want: model.ErrRetainRecentNegative,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := validateRemoveRequest(tt.req)

			// Assert
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

// TestPartitionByRefs pins what a remove ref reaches: an id every record, a name
// only the visible ones; a ref naming nothing or several fails the whole request.
func TestPartitionByRefs(t *testing.T) {
	record, backup := demoRecord(), demoSafetySnapshot()
	other := model.Savepoint{
		ID:        "019e8488-3ac8-7000-8000-000000000000",
		Name:      "other",
		CreatedAt: record.CreatedAt,
	}
	records := []model.Savepoint{record, backup, other}

	tests := []struct {
		name        string
		refs        []string
		wantKept    []model.Savepoint
		wantRemoved []model.Savepoint
		wantErr     string
	}{
		{
			name:        "by id",
			refs:        []string{record.ID.String()},
			wantKept:    []model.Savepoint{backup, other},
			wantRemoved: []model.Savepoint{record},
		},
		{
			name:        "by name",
			refs:        []string{"other"},
			wantKept:    []model.Savepoint{record, backup},
			wantRemoved: []model.Savepoint{other},
		},
		{
			name:        "an id and a name mixed",
			refs:        []string{"sp", other.ID.String()},
			wantKept:    []model.Savepoint{backup},
			wantRemoved: []model.Savepoint{record, other},
		},
		{
			name:        "one savepoint named twice is removed once",
			refs:        []string{"sp", record.ID.String()},
			wantKept:    []model.Savepoint{backup, other},
			wantRemoved: []model.Savepoint{record},
		},
		{
			name:        "an id reaches a safety snapshot",
			refs:        []string{backup.ID.String()},
			wantKept:    []model.Savepoint{record, other},
			wantRemoved: []model.Savepoint{backup},
		},
		{
			name:    "a name does not reach a safety snapshot",
			refs:    []string{backup.Name},
			wantErr: "savepoint(s) not found: " + backup.Name,
		},
		{
			name:    "every ref naming nothing is reported",
			refs:    []string{"nope", "sp", "019e8497-cf10-7000-8000-000000000000"},
			wantErr: "savepoint(s) not found: nope, 019e8497-cf10-7000-8000-000000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			kept, removed, err := partitionByRefs(records, tt.refs)

			// Assert
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, kept)
				assert.Nil(t, removed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKept, kept)
			assert.Equal(t, tt.wantRemoved, removed)
		})
	}
}

func TestPartitionByRefs_AmbiguousName(t *testing.T) {
	// Arrange
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	records := []model.Savepoint{
		{ID: "019e8488-3ac8-7000-8000-000000000000", Name: "dup", CreatedAt: now},
		{ID: "019e843e-1650-7000-8000-000000000000", Name: "dup", CreatedAt: now},
	}

	// Act
	kept, removed, err := partitionByRefs(records, []string{"dup"})

	// Assert
	var ambiguous *model.AmbiguousSavepointNameError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, []model.SavepointID{records[1].ID, records[0].ID}, ambiguous.IDs)
	assert.Nil(t, kept)
	assert.Nil(t, removed)
}

// TestService_parseRetentionCutoffs pins the two rules against each other: --retain-for
// counts back from the wall clock, --before-date is taken verbatim, and both
// together keep that order. A relative cutoff is checked against the clock the
// call itself read, so the tolerance is the test's own runtime.
func TestService_parseRetentionCutoffs(t *testing.T) {
	beforeDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		req       model.RemoveRequest
		want      []time.Duration // a cutoff relative to now; zero with an entry in wantAbs means "read it from wantAbs"
		wantAbs   map[int]time.Time
		wantErr   bool
		wantErrIs error
	}{
		{name: "no rules yields no cutoffs", req: model.RemoveRequest{}},
		{
			name: "retain-for subtracts the duration from now",
			req:  model.RemoveRequest{RetainFor: "2d"},
			want: []time.Duration{-48 * time.Hour},
		},
		{
			name:    "before-date is used verbatim",
			req:     model.RemoveRequest{BeforeDate: "2026-06-01T00:00:00Z"},
			want:    []time.Duration{0},
			wantAbs: map[int]time.Time{0: beforeDate},
		},
		{
			name:    "both rules yield both cutoffs, retain-for first",
			req:     model.RemoveRequest{RetainFor: "1h", BeforeDate: "2026-06-01T00:00:00Z"},
			want:    []time.Duration{-time.Hour, 0},
			wantAbs: map[int]time.Time{1: beforeDate},
		},
		{name: "unparsable retain-for errors", req: model.RemoveRequest{RetainFor: "yesterday"}, wantErr: true},
		{name: "unparsable before-date errors", req: model.RemoveRequest{BeforeDate: "june"}, wantErr: true},
		{
			name:      "negative retain-for is refused",
			req:       model.RemoveRequest{RetainFor: "-1h"},
			wantErr:   true,
			wantErrIs: model.ErrRetainForNegative,
		},
		{
			name: "zero retain-for cuts at now",
			req:  model.RemoveRequest{RetainFor: "0s"},
			want: []time.Duration{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := New(Deps{
				ArcaveProject: demoArcaveProject,
				SessionLocker: fakeSessionLocker{},
				Savepoints:    nil, ProjectLocker: nil,
				Inflight: &fakeInflight{},
			})
			now := time.Now()

			// Act
			got, err := svc.parseRetentionCutoffs(tt.req)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					require.ErrorIs(t, err, tt.wantErrIs)
				}
				return
			}
			require.NoError(t, err)
			require.Len(t, got, len(tt.want))
			for i, offset := range tt.want {
				if want, ok := tt.wantAbs[i]; ok {
					assert.True(t, got[i].Equal(want), "cutoff[%d] = %v, want %v", i, got[i], want)
					continue
				}
				assert.WithinDuration(t, now.Add(offset), got[i], time.Minute, "cutoff[%d] = %v", i, got[i])
			}
		})
	}
}

func TestPartitionSavepoints(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	mid := now.Add(-7 * 24 * time.Hour)
	fresh := now.Add(-time.Hour)
	records := recordsAt(old, mid, fresh)

	tests := []struct {
		name         string
		retainRecent int
		cutoffs      []time.Time
		wantRemoved  []string
	}{
		{
			name:         "retain recent keeps newest N",
			retainRecent: 2,
			wantRemoved:  []string{records[0].ID.String()},
		},
		{
			name:         "retain recent zero removes all",
			retainRecent: 0,
			wantRemoved:  ids(records),
		},
		{
			name:        "cutoff removes older only",
			cutoffs:     []time.Time{now.Add(-14 * 24 * time.Hour)},
			wantRemoved: []string{records[0].ID.String()},
		},
		{
			name:         "any rule retaining wins",
			retainRecent: 3,
			cutoffs:      []time.Time{now}, // cutoff alone would remove everything
			wantRemoved:  nil,
		},
		{
			name:         "removed only when all rules agree",
			retainRecent: 1,
			cutoffs:      []time.Time{now.Add(-14 * 24 * time.Hour)},
			wantRemoved:  []string{records[0].ID.String()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			kept, removed := partitionSavepoints(records, tt.retainRecent, tt.cutoffs)

			// Assert
			assert.Equal(t, tt.wantRemoved, ids(removed))
			assert.Len(t, kept, len(records)-len(removed), "kept + removed must cover every record")
		})
	}
}

// recordsAt plants one record per instant, each id a UUIDv7 of its creation time.
func recordsAt(times ...time.Time) []model.Savepoint {
	records := make([]model.Savepoint, len(times))
	for i, t := range times {
		records[i] = model.Savepoint{ID: savepointIDAt(t), CreatedAt: t}
	}
	return records
}

// savepointIDAt is the UUIDv7 a capture started at t would sort as.
func savepointIDAt(t time.Time) model.SavepointID {
	h := fmt.Sprintf("%012x", t.UnixMilli())
	return model.SavepointID(h[:8] + "-" + h[8:] + "-7000-8000-000000000000")
}

func ids(records []model.Savepoint) []string {
	if len(records) == 0 {
		return nil
	}
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID.String()
	}
	return out
}
