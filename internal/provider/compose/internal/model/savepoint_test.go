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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNewSavepointSummary(t *testing.T) {
	notStored := func(name string) model.Notice {
		return model.Notice{
			Message: "image " + name + " is not stored, only its identity",
			Hint:    "a rollback needs it pullable to rebuild a container on it",
			Severe:  true,
		}
	}
	volumesOnly := []model.UnitTotal{{Kind: model.UnitKindVolumes}}
	tests := []struct {
		name   string
		detail SavepointDetail
		want   model.SavepointSummary
	}{
		{name: "nothing held, nothing left out", want: model.SavepointSummary{Units: volumesOnly}},
		{
			name: "layers and images counted when held",
			detail: SavepointDetail{
				Stats: []SnapshotStat{{SizeBytes: 10, StoredBytes: 4}, {SizeBytes: 5, StoredBytes: 5}},
				WritableLayers: []WritableLayer{
					{Service: "db", Replica: 1, SizeBytes: 7, StoredBytes: 2},
					{Service: "db", Replica: 3},
				},
				Images: []Image{{SizeBytes: 100, Stored: true}},
			},
			want: model.SavepointSummary{
				Units: []model.UnitTotal{
					{Kind: model.UnitKindVolumes, Count: 2, SizeBytes: 15, StoredBytes: 9},
					{Kind: model.UnitKindLayers, Count: 2, SizeBytes: 7, StoredBytes: 2},
					{Kind: model.UnitKindImages, Count: 1, SizeBytes: 100},
				},
			},
		},
		{
			name: "an image not stored goes before what only informs",
			detail: SavepointDetail{
				NotCovered: &NotCovered{ServicesWithoutVolumes: []string{"web"}},
				Images: []Image{
					{ID: "sha256:0123456789abcdef", Stored: true, Refs: []string{"redis:7"}},
					{ID: "sha256:0123456789abcdef", Stored: false},
					{ID: "sha256:fedcba9876543210", Stored: false, Refs: []string{"postgres:16"}},
				},
			},
			want: model.SavepointSummary{
				Units: append(volumesOnly, model.UnitTotal{Kind: model.UnitKindImages, Count: 3}),
				Notices: []model.Notice{
					notStored("0123456789ab"),
					notStored("postgres:16"),
					{Message: "web has no volumes: only its writable layer is held"},
				},
			},
		},
		{
			name: "what is left out that is severe stays ahead of an image not stored",
			detail: SavepointDetail{
				NotCovered: &NotCovered{ReplicaGaps: []ReplicaGap{{Service: "db", Replicas: []int{1}, Missing: []int{2}}}},
				Images:     []Image{{ID: "sha256:fedcba9876543210", Refs: []string{"postgres:16"}}},
			},
			want: model.SavepointSummary{
				Units: append(volumesOnly, model.UnitTotal{Kind: model.UnitKindImages, Count: 1}),
				Notices: []model.Notice{
					{Message: "db: replica 2 gone, only 1 held", Severe: true},
					notStored("postgres:16"),
				},
			},
		},
		{
			name: "what was excluded is a notice",
			detail: SavepointDetail{NotCovered: &NotCovered{
				Mounts: []SkippedVolume{
					{Service: "db", Target: "/data", Volume: "demo_cache", Reason: SkipReasonExcluded},
					{Service: "web", Target: "/srv", RawType: string(VolumeTypeBind), Reason: SkipReasonExcluded},
					{Service: "web", Replica: 2, Target: "/tmp/x", Reason: SkipReasonExcluded},
				},
				WritableLayers: []SkippedWritableLayer{{Service: "web", Replica: 1}},
			}},
			want: model.SavepointSummary{
				Units: volumesOnly,
				Notices: (&NotCovered{
					Mounts: []SkippedVolume{
						{Service: "db", Target: "/data", Volume: "demo_cache", Reason: SkipReasonExcluded},
						{Service: "web", Target: "/srv", RawType: string(VolumeTypeBind), Reason: SkipReasonExcluded},
						{Service: "web", Replica: 2, Target: "/tmp/x", Reason: SkipReasonExcluded},
					},
					WritableLayers: []SkippedWritableLayer{{Service: "web", Replica: 1}},
				}).Notices(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewSavepointSummary(tt.detail)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// A record carries the header, the provider and the body, and no summary; it reads back whole.
func TestNewSavepointRecord(t *testing.T) {
	// Arrange
	savepoint := Savepoint{
		Header: model.Savepoint{
			ID:                    "019e843e-1650-7000-8000-000000000000",
			Name:                  "sp",
			Description:           "before the migration",
			Labels:                map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
			TakenBeforeRollbackTo: &model.SavepointRef{ID: "019e843e-1650-7000-8000-000000000001", Name: "v1"},
			CreatedAt:             time.Date(2026, 6, 1, 19, 41, 12, 0, time.UTC),
		},
		Detail: SavepointDetail{
			Stats: []SnapshotStat{{SizeBytes: 10}},
			WritableLayers: []WritableLayer{{
				Service:          "db",
				Replica:          1,
				ContainerName:    "demo-db-1",
				CommittedImageID: ImageDigestID("sha256:" + strings.Repeat("a", 64)),
			}},
			Images:     []Image{{ID: ImageDigestID("sha256:" + strings.Repeat("b", 64)), Refs: []string{"postgres:16"}, Stored: true}},
			Spec:       &SpecRef{Format: SpecFormatComposeYAML},
			NotCovered: &NotCovered{ServicesWithoutVolumes: []string{"web"}},
		},
	}

	// Act
	record, err := NewSavepointRecord(savepoint)
	require.NoError(t, err)
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	var decoded model.Savepoint
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	parsed, err := ParseSavepoint(decoded)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, ProviderName, record.Provider)
	assert.NotContains(t, string(encoded), `"summary"`)
	assert.Equal(t, savepoint, parsed)
}

// The shared savepoint is the record with its summary.
func TestToSharedSavepoint(t *testing.T) {
	// Arrange
	savepoint := Savepoint{
		Header: model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "sp"},
		Detail: SavepointDetail{Stats: []SnapshotStat{{SizeBytes: 10, StoredBytes: 4}}},
	}
	record, err := NewSavepointRecord(savepoint)
	require.NoError(t, err)

	// Act
	shared, err := ToSharedSavepoint(savepoint)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, NewSavepointSummary(savepoint.Detail), shared.Summary)
	assert.Equal(t, record.Detail, shared.Detail)
	assert.Equal(t, ProviderName, shared.Provider)
}

func TestParseSavepoint(t *testing.T) {
	tests := []struct {
		name    string
		record  model.Savepoint
		want    Savepoint
		wantErr error
	}{
		{
			name:   "the compose provider's with no body is an empty one",
			record: model.Savepoint{ID: "sp", Provider: ProviderName},
			want:   Savepoint{Header: model.Savepoint{ID: "sp"}},
		},
		{
			name: "a summary on the record is not kept: it is derived",
			record: model.Savepoint{
				ID:       "sp",
				Provider: ProviderName,
				Summary:  model.SavepointSummary{Units: []model.UnitTotal{{Kind: model.UnitKindVolumes, Count: 9}}},
				Detail:   json.RawMessage(`{"stats":[{"size_bytes":1}]}`),
			},
			want: Savepoint{
				Header: model.Savepoint{ID: "sp"},
				Detail: SavepointDetail{Stats: []SnapshotStat{{SizeBytes: 1}}},
			},
		},
		{name: "another provider's", record: model.Savepoint{ID: "sp", Provider: "kubernetes"}, wantErr: ErrSavepointDetail},
		{name: "no provider, an earlier version's record", record: model.Savepoint{ID: "sp"}, wantErr: ErrSavepointDetail},
		{
			name:    "a body that does not decode",
			record:  model.Savepoint{ID: "sp", Provider: ProviderName, Detail: json.RawMessage(`[]`)},
			wantErr: ErrSavepointDetail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseSavepoint(tt.record)

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, got)
		})
	}
}
