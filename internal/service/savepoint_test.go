package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/gm-tools/internal/model"
)

func recordsAt(times ...time.Time) []model.SavepointRecord {
	records := make([]model.SavepointRecord, len(times))
	for i, t := range times {
		records[i] = model.SavepointRecord{Savepoint: model.Savepoint{ID: t.Format("20060102T150405"), CreatedAt: t}}
	}
	return records
}

func ids(records []model.SavepointRecord) []string {
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
	records := []model.SavepointRecord{
		{Savepoint: model.Savepoint{ID: "100", Name: "before-migration", CreatedAt: base}},
		{Savepoint: model.Savepoint{ID: "300", Name: "dup", CreatedAt: base.Add(2 * time.Hour)}}, // newest
		{Savepoint: model.Savepoint{ID: "200", Name: "dup", CreatedAt: base.Add(time.Hour)}},
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
		name     string
		existing []model.SavepointRecord
		want     string
	}{
		{
			name: "no collision uses the plain unix second",
			want: "1780335122",
		},
		{
			name: "collision in the same second bumps forward until free",
			existing: []model.SavepointRecord{
				{Savepoint: model.Savepoint{ID: "1780335122"}},
				{Savepoint: model.Savepoint{ID: "1780335123"}},
			},
			want: "1780335124",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := uniqueSavepointID(now, tt.existing)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
