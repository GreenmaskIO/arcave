package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A safety snapshot is told from a savepoint by one label and nothing else, so the
// predicate is the whole of that distinction.
func TestSavepoint_IsSafetySnapshot(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "the label a rollback stamps", labels: map[string]string{SafetySnapshotLabel: SafetySnapshotLabelValue}, want: true},
		{
			name:   "beside labels of the user's own",
			labels: map[string]string{"team": "payments", SafetySnapshotLabel: SafetySnapshotLabelValue},
			want:   true,
		},
		{name: "no labels at all", labels: nil},
		{name: "other labels only", labels: map[string]string{"team": "payments"}},
		// A key match is not enough: only the value arcave writes counts, so a user
		// label that happens to share the key never hides a savepoint.
		{name: "the key with another value", labels: map[string]string{SafetySnapshotLabel: "false"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := Savepoint{ID: "1780335122", Labels: tt.labels}.IsSafetySnapshot()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
