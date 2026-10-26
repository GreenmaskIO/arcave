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
	"time"

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

// A restic stat's bytes are its estimates', never its ops': a forget alone frees nothing.
func TestResticRepositoryStat_Summary(t *testing.T) {
	size := func(n int64) *int64 { return &n }
	tests := []struct {
		name string
		stat ResticRepositoryStat
		want GarbageSummary
	}{
		{
			name: "no estimate frees nothing",
			stat: ResticRepositoryStat{GarbageFindings: GarbageFindings{Ops: []GarbageOp{}}},
			want: GarbageSummary{Bytes: size(0)},
		},
		{
			name: "estimates add up",
			stat: ResticRepositoryStat{
				GarbageFindings: GarbageFindings{Ops: []GarbageOp{{}, {}}, Unjudged: []GarbageSkip{{}}},
				Estimates:       []ResticForgetEstimate{{FreedBytes: size(3)}, {FreedBytes: size(4)}},
			},
			want: GarbageSummary{Found: 2, Bytes: size(7), Unjudged: 1},
		},
		{
			name: "one unknown estimate leaves the total unknown",
			stat: ResticRepositoryStat{
				GarbageFindings: GarbageFindings{Ops: []GarbageOp{{}, {}}},
				Estimates:       []ResticForgetEstimate{{FreedBytes: size(3)}, {}},
			},
			want: GarbageSummary{Found: 2},
		},
		{
			name: "an op's own bytes are not counted",
			stat: ResticRepositoryStat{
				GarbageFindings: GarbageFindings{Ops: []GarbageOp{{Bytes: size(100)}}},
				Estimates:       []ResticForgetEstimate{{FreedBytes: size(5)}},
			},
			want: GarbageSummary{Found: 1, Bytes: size(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.stat.Summary())
		})
	}
}

func TestGarbageDataKind_Validate(t *testing.T) {
	for _, dataKind := range GarbageDataKinds() {
		assert.NoError(t, dataKind.Validate())
	}
	assert.ErrorIs(t, GarbageDataKind("restic").Validate(), ErrInvalidGarbageCollectRequest)
}

func TestGarbageCollectRequest_Validate(t *testing.T) {
	hour := time.Hour
	negative := -time.Hour

	tests := []struct {
		name    string
		req     GarbageCollectRequest
		wantErr error
	}{
		{name: "nothing set", req: GarbageCollectRequest{DryRun: true}},
		{name: "known data kinds and a grace", req: GarbageCollectRequest{DataKinds: []GarbageDataKind{GarbageDataKindImageLayers}, RestoredLayerGrace: &hour}},
		{name: "an unknown data kind", req: GarbageCollectRequest{DataKinds: []GarbageDataKind{"blob"}}, wantErr: ErrInvalidGarbageCollectRequest},
		{
			name:    "session trees are not gc's: session list --all names them",
			req:     GarbageCollectRequest{DataKinds: []GarbageDataKind{"session-tree"}},
			wantErr: ErrInvalidGarbageCollectRequest,
		},
		{name: "a negative grace", req: GarbageCollectRequest{RestoredLayerGrace: &negative}, wantErr: ErrInvalidGarbageCollectRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.req.Validate()

			// Assert
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// The report is the wire contract: the detail is the kind's own stat, its findings inlined.
func TestGarbageReport_JSON(t *testing.T) {
	// Arrange
	size := int64(5)
	report := GarbageReport{
		DataKind: GarbageDataKindImageLayers, Collector: GarbageCollectorKindOCIBlobs, ComposeProject: "demo",
		Summary: GarbageSummary{Found: 1, Bytes: &size},
		Detail: OCIBlobStat{
			GarbageFindings: GarbageFindings{Ops: []GarbageOp{{
				Action: GarbageActionDeleteObject, Target: "shared/oci/blobs/sha256/ab", Bytes: &size,
				Reason: GarbageReasonUnreferenced,
			}}},
			Live: 2,
		},
	}

	// Act
	got, err := json.Marshal(report)

	// Assert
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"data_kind": "image-layers", "collector": "oci-blobs", "compose_project": "demo",
		"summary": {"found": 1, "bytes": 5, "skipped": 0, "unjudged": 0},
		"detail": {
			"ops": [{"action": "delete-object", "target": "shared/oci/blobs/sha256/ab", "bytes": 5, "reason": "unreferenced"}],
			"live": 2
		}
	}`, string(got))
}

func TestGarbageDeletion_FreedBytes(t *testing.T) {
	size := func(n int64) *int64 { return &n }
	tests := []struct {
		name     string
		deletion GarbageDeletion
		want     *int64
	}{
		{name: "nothing deleted frees nothing", deletion: GarbageDeletion{Deleted: []GarbageDeleted{}}, want: size(0)},
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
