package core

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func recordsAt(times ...time.Time) []model.Savepoint {
	records := make([]model.Savepoint, len(times))
	for i, t := range times {
		records[i] = model.Savepoint{ID: t.Format("20060102T150405"), CreatedAt: t}
	}
	return records
}

func ids(records []model.Savepoint) []string {
	if len(records) == 0 {
		return nil
	}
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID
	}
	return out
}

func TestFindSavepoint(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	records := []model.Savepoint{
		{ID: "100", Name: "before-migration", CreatedAt: base},
		{ID: "300", Name: "dup", CreatedAt: base.Add(2 * time.Hour)}, // newest
		{ID: "200", Name: "dup", CreatedAt: base.Add(time.Hour)},
	}

	tests := []struct {
		name    string
		ref     string
		wantID  string
		wantErr bool
	}{
		{name: "latest picks newest by created_at", ref: "latest", wantID: "300"},
		{name: "explicit id hit", ref: "200", wantID: "200"},
		{name: "unique name hit", ref: "before-migration", wantID: "100"},
		{name: "ambiguous name errors", ref: "dup", wantErr: true},
		{name: "unknown ref errors", ref: "999", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := findSavepoint(records, tt.ref)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}

	t.Run("empty manifest errors", func(t *testing.T) {
		// Act
		_, err := findSavepoint(nil, "latest")

		// Assert
		require.Error(t, err)
	})
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
			wantRemoved:  []string{records[0].ID},
		},
		{
			name:         "retain recent zero removes all",
			retainRecent: 0,
			wantRemoved:  ids(records),
		},
		{
			name:        "cutoff removes older only",
			cutoffs:     []time.Time{now.Add(-14 * 24 * time.Hour)},
			wantRemoved: []string{records[0].ID},
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
			wantRemoved:  []string{records[0].ID},
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

func TestUniqueSavepointID(t *testing.T) {
	now := time.Unix(1780335122, 0)

	tests := []struct {
		name string
		want string
	}{
		{
			name: "no collision uses the plain unix second",
			want: "1780335122",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := uniqueSavepointID(now)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestService_RemoveSavepoints covers what a removal reports. A savepoint whose
// data could not be deleted has already been uncommitted, so it is gone from the
// listing and its namespace is orphaned — the error must name it.
func TestService_RemoveSavepoints(t *testing.T) {
	tests := []struct {
		name        string
		deleteErr   map[string]error // savepoint id -> error returned by repo.Delete
		removeErr   map[string]error // savepoint id -> error returned by backend.Remove
		wantErr     string
		wantRemoved []string // ids reported back to the caller
		// wantDataRemoved lists the ids whose data the backend must be asked to
		// drop. An id whose uncommit failed must never reach the backend.
		wantDataRemoved []string
	}{
		{
			name:            "all removals succeed",
			wantRemoved:     []string{"1", "2"},
			wantDataRemoved: []string{"1", "2"},
		},
		{
			name:            "a failed data delete orphans the namespace",
			removeErr:       map[string]error{"2": errors.New("delete boom")},
			wantErr:         "remove snapshot 2: delete boom",
			wantDataRemoved: []string{"1", "2"},
		},
		{
			name:            "a failed uncommit never touches the data",
			deleteErr:       map[string]error{"2": errors.New("record boom")},
			wantErr:         "uncommit savepoint 2: record boom",
			wantDataRemoved: []string{"1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			records := []model.Savepoint{
				{ID: "1"},
				{ID: "2"},
				{ID: "3"},
			}
			repo := &mockSavepointRepo{}
			repo.On("List").Return(records, nil)

			savepointBackend := &mockBackend{}
			for _, id := range []string{"1", "2"} {
				repo.On("Delete", id).Return(tt.deleteErr[id])
				if slices.Contains(tt.wantDataRemoved, id) {
					savepointBackend.On("Remove", mock.Anything, id).Return(tt.removeErr[id])
				}
			}
			// Removal touches only the repository and the savepoint backend; the
			// compose clients and the safety-backup backend stay nil.
			svc := New(repo, freeLocker(), nil, nil, savepointBackend, nil)
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			res, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []string{"1", "2"}})

			// Assert
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantRemoved, ids(res.Removed))
			}
			// An id whose uncommit failed must never reach the backend: its record
			// still names data that is still there, so the savepoint is intact.
			for _, id := range []string{"1", "2"} {
				if !slices.Contains(tt.wantDataRemoved, id) {
					savepointBackend.AssertNotCalled(t, "Remove", mock.Anything, id)
				}
			}
			savepointBackend.AssertExpectations(t)
			repo.AssertExpectations(t)
		})
	}
}

// TestService_RemoveSavepoints_UncommitsBeforeDeletingData pins the ordering that
// keeps savepoint.json safe inside a namespace the backend RemoveAlls. The record
// must be deleted as its own operation, on its own object, before any data is
// touched: a crash between the two must leave an orphan (invisible, collectable),
// never a savepoint that still lists and still resolves but whose archives are
// half gone. Reversing the two calls would compile clean and pass every other
// test in this file.
func TestService_RemoveSavepoints_UncommitsBeforeDeletingData(t *testing.T) {
	// Arrange
	var calls []string
	repo := &mockSavepointRepo{}
	repo.On("List").Return([]model.Savepoint{{ID: "1"}}, nil)
	repo.On("Delete", "1").Run(func(mock.Arguments) { calls = append(calls, "uncommit") }).Return(nil)

	savepointBackend := &mockBackend{}
	savepointBackend.On("Remove", mock.Anything, "1").
		Run(func(mock.Arguments) { calls = append(calls, "remove data") }).Return(nil)

	svc := New(repo, freeLocker(), nil, nil, savepointBackend, nil)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := svc.RemoveSavepoints(ctx, model.RemoveRequest{IDs: []string{"1"}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, []string{"uncommit", "remove data"}, calls)
}

// createFixture wires a Service whose collaborators are all mocks, with the
// clock fixed so the minted savepoint id is exact.
type createFixture struct {
	svc       *Service
	repo      *mockSavepointRepo
	inspector *mockInspector
	backend   *mockBackend
	runner    *mockComposeRunner
	calls     *[]string // every backend/repo call, in the order it happened
	savepnts  string    // the savepoint id (and its storage directory)
}

func newCreateFixture(t *testing.T) createFixture {
	t.Helper()

	var calls []string
	id := strconv.FormatInt(fixedNow.Unix(), 10)

	// No List expectation: create mints its id from the clock alone and never
	// reads the store — see uniqueSavepointID.
	repo := &mockSavepointRepo{}

	inspector := &mockInspector{}
	inspector.On("Introspect", mock.Anything, mock.Anything).Return(demoIntrospection(), nil)

	runner := &mockComposeRunner{}
	runner.On("Stop", mock.Anything, mock.Anything).Return(nil)
	runner.On("Up", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	backend := &mockBackend{}
	backend.On("Remove", mock.Anything, id).
		Run(func(mock.Arguments) { calls = append(calls, "wipe directory") }).Return(nil)

	svc := New(repo, freeLocker(), inspector, runner, backend, nil)
	svc.now = func() time.Time { return fixedNow }

	return createFixture{
		svc: svc, repo: repo, inspector: inspector, backend: backend,
		runner: runner, calls: &calls, savepnts: id,
	}
}

// TestService_CreateSavepoint pins the ordering that makes the record a commit
// marker: the namespace is wiped before any archive is written, and the record is
// saved *last*. A crash before the save leaves data no record names — invisible
// to List, collectable — rather than a savepoint that lists without its data.
//
// It also pins the two ends: the units FindBackupVolumes derived reach the
// backend, and the stats it returns are committed verbatim.
func TestService_CreateSavepoint(t *testing.T) {
	// Arrange
	f := newCreateFixture(t)
	stats := demoStats()
	f.backend.On("Snapshot", mock.Anything, f.savepnts, demoProject, []model.Volume{demoVolume()}).
		Run(func(mock.Arguments) { *f.calls = append(*f.calls, "write archives") }).
		Return(stats, nil)
	var committed model.Savepoint
	f.repo.On("Save", mock.Anything).
		Run(func(args mock.Arguments) {
			*f.calls = append(*f.calls, "commit record")
			committed = args.Get(0).(model.Savepoint)
		}).Return(nil)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	sp, err := f.svc.CreateSavepoint(ctx, model.CreateSavepointRequest{Name: "a"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, strconv.FormatInt(fixedNow.Unix(), 10), sp.ID)
	assert.Equal(t, []string{"wipe directory", "write archives", "commit record"}, *f.calls)
	assert.Equal(t, stats, committed.Stats, "the committed record carries the backend's stats")
	assert.Equal(t, stats, sp.Stats, "and so does the savepoint returned to the caller")
	f.backend.AssertExpectations(t)
	f.repo.AssertExpectations(t)
}

// A named volume several services mount is one unit: snapshotted once, recorded
// once, where v1 captured it per mount. Nothing else here would notice.
func TestService_CreateSavepoint_SnapshotsASharedVolumeOnce(t *testing.T) {
	// Arrange
	f := newCreateFixture(t)
	shared := model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{
			{Name: "api", Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/srv/assets", VolumeName: "demo_assets"},
			}},
			{Name: "web", Volumes: []model.ServiceVolume{
				{Type: model.VolumeTypeNamed, RawType: "volume", Source: "assets", Target: "/usr/share/nginx/html", VolumeName: "demo_assets"},
			}},
		},
		Volumes: []model.NamedVolume{{
			Key:  "assets",
			Name: "demo_assets",
			UsedBy: []model.ServiceReference{
				{Service: "api", Target: "/srv/assets"},
				{Service: "web", Target: "/usr/share/nginx/html"},
			},
		}},
	}}
	f.inspector.ExpectedCalls = nil
	f.inspector.On("Introspect", mock.Anything, mock.Anything).Return(shared, nil)

	var snapshotted []model.Volume
	f.backend.On("Snapshot", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { snapshotted = args.Get(3).([]model.Volume) }).
		Return([]model.SnapshotStat{{Volume: demoVolume()}}, nil)
	f.repo.On("Save", mock.Anything).Return(nil)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := f.svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, snapshotted, 1, "one volume, however many services mount it")
	assert.Equal(t, "volume/demo_assets", snapshotted[0].ObjectID)
	assert.Len(t, snapshotted[0].Named.UsedBy, 2, "both mounts travel with the one unit")
}

// A project whose every volume is unreachable — a named volume nobody mounts —
// has nothing to capture, so the create fails before the services are stopped.
func TestService_CreateSavepoint_NothingToSnapshot(t *testing.T) {
	// Arrange
	f := newCreateFixture(t)
	f.inspector.ExpectedCalls = nil
	f.inspector.On("Introspect", mock.Anything, mock.Anything).Return(model.IntrospectionResult{
		Project: model.Project{
			Name:    demoProject,
			Volumes: []model.NamedVolume{{Key: "orphan", Name: "demo_orphan"}},
		},
	}, nil)
	ctx := zerolog.Nop().WithContext(context.Background())

	// Act
	_, err := f.svc.CreateSavepoint(ctx, model.CreateSavepointRequest{})

	// Assert
	require.ErrorContains(t, err, "nothing to snapshot")
	f.runner.AssertNotCalled(t, "Stop", mock.Anything, mock.Anything)
	f.backend.AssertNotCalled(t, "Snapshot", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// A snapshot that fails partway leaves half-written archives no record names yet,
// so the whole directory is dropped — the only thing that can clean them up.
func TestService_CreateSavepoint_DropsTheNamespaceOnFailure(t *testing.T) {
	tests := []struct {
		name       string
		createErr  error // backend.Snapshot fails: a mount could not be exported
		saveErr    error // repo.Save fails: the commit could not be written
		wantErrMsg string
	}{
		{name: "an archive fails to write", createErr: errors.New("export boom"), wantErrMsg: "export boom"},
		{name: "the commit fails to write", saveErr: errors.New("commit boom"), wantErrMsg: "commit boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			f := newCreateFixture(t)
			var stats []model.SnapshotStat
			if tt.createErr == nil {
				stats = demoStats() // one stat per captured volume
			}
			f.backend.On("Snapshot", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				Return(stats, tt.createErr)
			if tt.createErr == nil {
				f.repo.On("Save", mock.Anything).Return(tt.saveErr)
			}
			ctx := zerolog.Nop().WithContext(context.Background())

			// Act
			_, err := f.svc.CreateSavepoint(ctx, model.CreateSavepointRequest{Name: "a"})

			// Assert
			require.ErrorContains(t, err, tt.wantErrMsg)
			// Twice: once to wipe before writing, once to drop the partial snapshot.
			f.backend.AssertNumberOfCalls(t, "Remove", 2)
			// The services must come back up whatever happened to the snapshot.
			f.runner.AssertCalled(t, "Up", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestValidateRemoveRequest covers the selector rules. A negative --retain-recent
// must be reported as such: it used to fall through to hasRetention (which counts
// only a positive count) and surface as model.ErrRemoveNoSelector.
func TestValidateRemoveRequest(t *testing.T) {
	tests := []struct {
		name string
		req  model.RemoveRequest
		want error
	}{
		{name: "explicit ids", req: model.RemoveRequest{IDs: []string{"1"}}},
		{name: "retain recent", req: model.RemoveRequest{RetainRecent: 2}},
		{name: "retain for", req: model.RemoveRequest{RetainFor: "1d"}},
		{
			name: "ids and retention conflict",
			req:  model.RemoveRequest{IDs: []string{"1"}, RetainRecent: 2},
			want: model.ErrRemoveSelectorConflict,
		},
		{name: "no selector at all", req: model.RemoveRequest{}, want: model.ErrRemoveNoSelector},
		{
			name: "negative retain recent",
			req:  model.RemoveRequest{RetainRecent: -1},
			want: model.ErrRetainRecentNegative,
		},
		{
			name: "negative retain recent beats a conflicting id list",
			req:  model.RemoveRequest{IDs: []string{"1"}, RetainRecent: -1},
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

// TestService_retentionCutoffs pins --retain-for against the injected clock, so
// the cutoff is exact rather than "roughly now minus a day".
func TestService_retentionCutoffs(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		req     model.RemoveRequest
		want    []time.Time
		wantErr bool
	}{
		{name: "no rules yields no cutoffs", req: model.RemoveRequest{}},
		{
			name: "retain-for subtracts the duration from now",
			req:  model.RemoveRequest{RetainFor: "2d"},
			want: []time.Time{now.AddDate(0, 0, -2)},
		},
		{
			name: "before-date is used verbatim",
			req:  model.RemoveRequest{BeforeDate: "2026-06-01T00:00:00Z"},
			want: []time.Time{time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		},
		{
			name: "both rules yield both cutoffs, retain-for first",
			req:  model.RemoveRequest{RetainFor: "1h", BeforeDate: "2026-06-01T00:00:00Z"},
			want: []time.Time{
				now.Add(-time.Hour),
				time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{name: "unparsable retain-for errors", req: model.RemoveRequest{RetainFor: "yesterday"}, wantErr: true},
		{name: "unparsable before-date errors", req: model.RemoveRequest{BeforeDate: "june"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			svc := New(nil, nil, nil, nil, nil, nil)
			svc.now = func() time.Time { return now }

			// Act
			got, err := svc.retentionCutoffs(tt.req)

			// Assert
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, len(tt.want))
			for i, want := range tt.want {
				assert.True(t, got[i].Equal(want), "cutoff[%d] = %v, want %v", i, got[i], want)
			}
		})
	}
}
