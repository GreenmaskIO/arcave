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

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGarbageFindings_Summary(t *testing.T) {
	size := func(n int64) *int64 { return &n }
	tests := []struct {
		name     string
		findings GarbageFindings
		want     GarbageSummary
	}{
		{
			name:     "nothing found frees nothing",
			findings: GarbageFindings{Ops: []GarbageOp{}},
			want:     GarbageSummary{Bytes: size(0)},
		},
		{
			name: "sizes add up",
			findings: GarbageFindings{
				Ops:     []GarbageOp{{Bytes: size(3)}, {Bytes: size(4)}},
				Skipped: []GarbageSkip{{}},
			},
			want: GarbageSummary{Found: 2, Bytes: size(7), Skipped: 1},
		},
		{
			name:     "one unknown size leaves the total unknown",
			findings: GarbageFindings{Ops: []GarbageOp{{Bytes: size(3)}, {}}},
			want:     GarbageSummary{Found: 2},
		},
		{
			name:     "an aborted kind says why",
			findings: GarbageFindings{Ops: []GarbageOp{}, Aborted: "unreadable"},
			want:     GarbageSummary{Bytes: size(0), Aborted: "unreadable"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.findings.Summary())
		})
	}
}

// The report is the wire contract: the detail is the kind's own stat, its findings inlined.
func TestGarbageReport_JSON(t *testing.T) {
	// Arrange
	size := int64(5)
	report := GarbageReport{
		DataKind: GarbageDataKindSafetySnapshots, Collector: GarbageCollectorKindSafetySnapshotRetention,
		ComposeProject: "demo",
		Summary:        GarbageSummary{Found: 1, Bytes: &size},
		Detail: SafetySnapshotStat{
			GarbageFindings: GarbageFindings{Ops: []GarbageOp{{
				Action: GarbageActionDeleteSavepoint, Target: "sp1", Bytes: &size,
				Reason: GarbageReasonBeyondRetention,
			}}},
			Retained: 2,
		},
	}

	// Act
	got, err := json.Marshal(report)

	// Assert
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"data_kind": "safety-snapshots", "collector": "safety-snapshot-retention", "compose_project": "demo",
		"summary": {"found": 1, "bytes": 5, "skipped": 0, "unjudged": 0},
		"detail": {
			"ops": [{"action": "delete-savepoint", "target": "sp1", "bytes": 5, "reason": "beyond-retention"}],
			"retained": 2
		}
	}`, string(got))
}

func TestGarbageDeletion_IsInterrupted(t *testing.T) {
	tests := []struct {
		name     string
		deletion GarbageDeletion
		want     bool
	}{
		{name: "every op deleted", deletion: GarbageDeletion{Deleted: []GarbageDeleted{{Target: "a"}}}},
		{name: "a failure of its own", deletion: GarbageDeletion{Failed: []GarbageDeleteFailure{{Target: "a"}}}},
		{name: "an op not run", deletion: GarbageDeletion{NotRun: 1}, want: true},
		{
			name: "a failure the stop cut short",
			deletion: GarbageDeletion{Failed: []GarbageDeleteFailure{
				{Target: "a"}, {Target: "b", Interrupted: true},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.deletion.IsInterrupted()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGarbageDeletion_FreedBytes(t *testing.T) {
	size := func(n int64) *int64 { return &n }
	tests := []struct {
		name     string
		deletion GarbageDeletion
		want     *int64
	}{
		{name: "nothing deleted frees nothing", deletion: GarbageDeletion{}, want: size(0)},
		{
			name: "every size known",
			deletion: GarbageDeletion{Deleted: []GarbageDeleted{
				{Target: "a", Bytes: size(1)}, {Target: "b", Bytes: size(2)},
			}},
			want: size(3),
		},
		{
			name: "a size unknown",
			deletion: GarbageDeletion{Deleted: []GarbageDeleted{
				{Target: "a", Bytes: size(1)}, {Target: "b"},
			}},
		},
		{
			name: "a prune's reclaimed bytes add to the ops'",
			deletion: GarbageDeletion{
				Deleted:   []GarbageDeleted{{Target: "a", Bytes: size(0)}},
				Reclaimed: []GarbageReclaimed{{Target: "r", Bytes: size(5)}},
			},
			want: size(5),
		},
		{
			name: "a reclaimed size unknown",
			deletion: GarbageDeletion{
				Deleted:   []GarbageDeleted{{Target: "a", Bytes: size(0)}},
				Reclaimed: []GarbageReclaimed{{Target: "r"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := tt.deletion.FreedBytes()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// The known sizes add up; each unknown one, an op's or a prune's, is counted instead.
func TestGarbageDeletion_KnownFreedBytes(t *testing.T) {
	size := func(n int64) *int64 { return &n }
	tests := []struct {
		name        string
		deletion    GarbageDeletion
		wantKnown   int64
		wantUnknown int
	}{
		{name: "nothing deleted", deletion: GarbageDeletion{}},
		{
			name: "every size known",
			deletion: GarbageDeletion{
				Deleted:   []GarbageDeleted{{Target: "a", Bytes: size(1)}, {Target: "b", Bytes: size(2)}},
				Reclaimed: []GarbageReclaimed{{Target: "r", Bytes: size(5)}},
			},
			wantKnown: 8,
		},
		{
			name: "the known part, and what is not",
			deletion: GarbageDeletion{
				Deleted:   []GarbageDeleted{{Target: "a", Bytes: size(1)}, {Target: "b"}},
				Reclaimed: []GarbageReclaimed{{Target: "r"}},
			},
			wantKnown:   1,
			wantUnknown: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			known, unknown := tt.deletion.KnownFreedBytes()

			// Assert
			assert.Equal(t, tt.wantKnown, known)
			assert.Equal(t, tt.wantUnknown, unknown)
		})
	}
}

func TestGarbageCollectResult_IsPartial(t *testing.T) {
	tests := []struct {
		name  string
		total GarbageTotal
		want  bool
	}{
		{name: "everything judged and deleted", total: GarbageTotal{Found: 2, Skipped: 1, Deleted: 2}},
		{name: "a report failed", total: GarbageTotal{Failed: 1}, want: true},
		{name: "a report aborted", total: GarbageTotal{Aborted: 1}, want: true},
		{name: "an object unjudged", total: GarbageTotal{Unjudged: 1}, want: true},
		{name: "a deletion failed", total: GarbageTotal{DeleteFailed: 1}, want: true},
		{name: "a deletion not run", total: GarbageTotal{NotRun: 1}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := GarbageCollectResult{Total: tt.total}.IsPartial()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}
