package cmd

import (
	"os"
	"testing"
	"time"
)

func recordsAt(times ...time.Time) []savepointRecord {
	records := make([]savepointRecord, len(times))
	for i, t := range times {
		records[i] = savepointRecord{ID: t.Format("20060102T150405"), CreatedAt: t}
	}
	return records
}

func ids(records []savepointRecord) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID
	}
	return out
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

func TestManifestRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())

	// Missing manifest reads as empty.
	m, err := loadManifest()
	if err != nil {
		t.Fatalf("loadManifest on missing file: %v", err)
	}
	if len(m.Savepoints) != 0 {
		t.Fatalf("expected empty manifest, got %d savepoints", len(m.Savepoints))
	}

	if err := os.MkdirAll(gmDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m.Savepoints = append(m.Savepoints, savepointRecord{
		ID:        "1780335122",
		Name:      "before-migration",
		CreatedAt: time.Date(2026, 6, 1, 19, 41, 12, 0, time.UTC),
		Volumes: []savepointVolume{{
			Service:   "db",
			MountPath: "/var/lib/postgresql/data",
			Type:      "volume",
			Source:    "myapp_pgdata",
			Path:      ".gm/snapshots/1780335122/db/var/lib/postgresql/data/data.tar",
		}},
	})
	if err := saveManifest(m); err != nil {
		t.Fatalf("saveManifest: %v", err)
	}

	got, err := loadManifest()
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if len(got.Savepoints) != 1 || got.Savepoints[0].ID != "1780335122" {
		t.Fatalf("unexpected manifest after round trip: %+v", got)
	}
	if got.Savepoints[0].Volumes[0].Source != "myapp_pgdata" {
		t.Fatalf("volume ref lost in round trip: %+v", got.Savepoints[0])
	}
}

func TestUniqueSavepointID(t *testing.T) {
	now := time.Unix(1780335122, 0)

	// No collision: the plain Unix second is used.
	if got := uniqueSavepointID(now, nil); got != "1780335122" {
		t.Errorf("uniqueSavepointID with no existing = %q, want %q", got, "1780335122")
	}

	// Collision in the same second bumps forward until free.
	existing := []savepointRecord{{ID: "1780335122"}, {ID: "1780335123"}}
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
