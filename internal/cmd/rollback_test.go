package cmd

import (
	"testing"
	"time"
)

func TestFindSavepoint(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	m := &manifest{Savepoints: []savepointRecord{
		{ID: "100", Name: "before-migration", CreatedAt: base},
		{ID: "300", Name: "dup", CreatedAt: base.Add(2 * time.Hour)}, // newest
		{ID: "200", Name: "dup", CreatedAt: base.Add(time.Hour)},
	}}

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
			got, err := findSavepoint(m, tt.ref)
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
		if _, err := findSavepoint(&manifest{}, "latest"); err == nil {
			t.Fatal("expected error for empty manifest")
		}
	})
}
