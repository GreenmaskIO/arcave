package service

import (
	"testing"
	"time"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/manifest"
)

func recordsAt(times ...time.Time) []manifest.Record {
	records := make([]manifest.Record, len(times))
	for i, t := range times {
		records[i] = manifest.Record{Savepoint: model.Savepoint{ID: t.Format("20060102T150405"), CreatedAt: t}}
	}
	return records
}

func ids(records []manifest.Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID
	}
	return out
}

func TestFindSavepoint(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	records := []manifest.Record{
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
			got, err := findSavepoint(records, tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for ref %q, got record %q", tt.ref, got.ID)
				}
				return
			}
			if err != nil {
				t.Fatalf("findSavepoint(%q): %v", tt.ref, err)
			}
			if got.ID != tt.wantID {
				t.Errorf("findSavepoint(%q) = %q, want %q", tt.ref, got.ID, tt.wantID)
			}
		})
	}

	t.Run("empty manifest errors", func(t *testing.T) {
		if _, err := findSavepoint(nil, "latest"); err == nil {
			t.Fatal("expected error for empty manifest")
		}
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
		hasRecent    bool
		retainRecent int
		cutoffs      []time.Time
		wantRemoved  []string
	}{
		{
			name:         "retain recent keeps newest N",
			hasRecent:    true,
			retainRecent: 2,
			wantRemoved:  []string{records[0].ID},
		},
		{
			name:         "retain recent zero removes all",
			hasRecent:    true,
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
			hasRecent:    true,
			retainRecent: 3,
			cutoffs:      []time.Time{now}, // cutoff alone would remove everything
			wantRemoved:  nil,
		},
		{
			name:         "removed only when all rules agree",
			hasRecent:    true,
			retainRecent: 1,
			cutoffs:      []time.Time{now.Add(-14 * 24 * time.Hour)},
			wantRemoved:  []string{records[0].ID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, removed := partitionSavepoints(records, tt.hasRecent, tt.retainRecent, tt.cutoffs)
			if got := ids(removed); !equal(got, tt.wantRemoved) {
				t.Errorf("removed = %v, want %v", got, tt.wantRemoved)
			}
			if len(kept)+len(removed) != len(records) {
				t.Errorf("kept(%d) + removed(%d) != total(%d)", len(kept), len(removed), len(records))
			}
		})
	}
}

func TestUniqueSavepointID(t *testing.T) {
	now := time.Unix(1780335122, 0)

	// No collision: the plain Unix second is used.
	if got := uniqueSavepointID(now, nil); got != "1780335122" {
		t.Errorf("uniqueSavepointID with no existing = %q, want %q", got, "1780335122")
	}

	// Collision in the same second bumps forward until free.
	existing := []manifest.Record{
		{Savepoint: model.Savepoint{ID: "1780335122"}},
		{Savepoint: model.Savepoint{ID: "1780335123"}},
	}
	if got := uniqueSavepointID(now, existing); got != "1780335124" {
		t.Errorf("uniqueSavepointID with collisions = %q, want %q", got, "1780335124")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
