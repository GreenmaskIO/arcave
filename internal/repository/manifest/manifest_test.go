package manifest

import (
	"testing"
	"time"

	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/snapshot"
)

func TestManifestRoundTrip(t *testing.T) {
	repo := New(t.TempDir())

	// Missing manifest reads as empty.
	m, err := repo.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if len(m.Savepoints) != 0 {
		t.Fatalf("expected empty manifest, got %d savepoints", len(m.Savepoints))
	}

	m.Savepoints = append(m.Savepoints, Record{
		Savepoint: model.Savepoint{
			ID:        "1780335122",
			Name:      "before-migration",
			CreatedAt: time.Date(2026, 6, 1, 19, 41, 12, 0, time.UTC),
		},
		Volumes: []snapshot.VolumeRef{{
			Service:   "db",
			MountPath: "/var/lib/postgresql/data",
			Type:      "volume",
			Source:    "myapp_pgdata",
			Snapshot: snapshot.SnapshotRef{
				Backend: "copy",
				Opaque:  ".gm/snapshots/1780335122/db/var/lib/postgresql/data/data.tar",
			},
		}},
	})
	if err := repo.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Savepoints) != 1 || got.Savepoints[0].ID != "1780335122" {
		t.Fatalf("unexpected manifest after round trip: %+v", got)
	}
	vol := got.Savepoints[0].Volumes[0]
	if vol.Source != "myapp_pgdata" {
		t.Fatalf("volume ref lost in round trip: %+v", got.Savepoints[0])
	}
	if vol.Snapshot.Backend != "copy" || vol.Snapshot.Opaque == "" {
		t.Fatalf("snapshot ref lost in round trip: %+v", vol)
	}
}
