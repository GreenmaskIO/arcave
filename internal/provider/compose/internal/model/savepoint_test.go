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

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNewSavepointSummary_Units(t *testing.T) {
	tests := []struct {
		name   string
		detail SavepointDetail
		want   []model.UnitTally
	}{
		{name: "volumes always", want: []model.UnitTally{{Kind: model.UnitKindVolumes}}},
		{
			name: "layers and images when held",
			detail: SavepointDetail{
				Stats:          []SnapshotStat{{SizeBytes: 10, StoredBytes: 4}, {SizeBytes: 5, StoredBytes: 5}},
				WritableLayers: []WritableLayer{{SizeBytes: 7, StoredBytes: 2}},
				Images:         []Image{{SizeBytes: 100, StoredBytes: 0, Stored: true}},
			},
			want: []model.UnitTally{
				{Kind: model.UnitKindVolumes, Count: 2, SizeBytes: 15, StoredBytes: 9},
				{Kind: model.UnitKindLayers, Count: 1, SizeBytes: 7, StoredBytes: 2},
				{Kind: model.UnitKindImages, Count: 1, SizeBytes: 100},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewSavepointSummary(tt.detail).Units

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewSavepointSummary_Notices(t *testing.T) {
	tests := []struct {
		name   string
		detail SavepointDetail
		want   []model.Notice
	}{
		{
			name: "nothing left out",
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
			want: []model.Notice{
				{
					Message: "image 0123456789ab is not stored, only its identity",
					Hint:    "a rollback needs it pullable to rebuild a container on it",
					Severe:  true,
				},
				{
					Message: "image postgres:16 is not stored, only its identity",
					Hint:    "a rollback needs it pullable to rebuild a container on it",
					Severe:  true,
				},
				{Message: "web has no volumes: only its writable layer is held"},
			},
		},
		{
			name: "an image not stored with nothing else left out",
			detail: SavepointDetail{
				Images: []Image{{ID: "sha256:fedcba9876543210", Refs: []string{"postgres:16"}}},
			},
			want: []model.Notice{
				{
					Message: "image postgres:16 is not stored, only its identity",
					Hint:    "a rollback needs it pullable to rebuild a container on it",
					Severe:  true,
				},
			},
		},
		{
			name: "what is left out that is severe stays ahead of an image not stored",
			detail: SavepointDetail{
				NotCovered: &NotCovered{ReplicaGaps: []ReplicaGap{{Service: "db", Replicas: []int{1}, Missing: []int{2}}}},
				Images:     []Image{{ID: "sha256:fedcba9876543210", Refs: []string{"postgres:16"}}},
			},
			want: []model.Notice{
				{
					Message: "db: replica 2 gone, only 1 held",
					Severe:  true,
				},
				{
					Message: "image postgres:16 is not stored, only its identity",
					Hint:    "a rollback needs it pullable to rebuild a container on it",
					Severe:  true,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := NewSavepointSummary(tt.detail).Notices

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewSavepointSummary_Gaps(t *testing.T) {
	// Arrange
	notCovered := NotCovered{
		Mounts: []SkippedVolume{
			{Service: "db", Target: "/data", Volume: "demo_cache", Reason: SkipReasonExcluded},
			{Service: "web", Target: "/srv", RawType: string(VolumeTypeBind), Reason: SkipReasonExcluded},
			{Service: "web", Replica: 2, Target: "/tmp/x", Reason: SkipReasonExcluded},
			{Service: "web", Target: "/run", Reason: SkipReasonTmpfs},
		},
		WritableLayers: []SkippedWritableLayer{{Service: "web", Replica: 1}},
	}

	// Act
	notices := NewSavepointSummary(SavepointDetail{NotCovered: &notCovered}).Gaps

	// Assert
	assert.Equal(t, []model.Notice{
		{Code: model.NoticeCodeVolumeNotRestored,
			Message: "volume demo_cache was not in the savepoint: it holds what it held before the rollback"},
		{Code: model.NoticeCodeVolumeNotRestored,
			Message: "web /srv was not in the savepoint: the host folder holds what it held before the rollback"},
		{Code: model.NoticeCodeVolumeNotRestored, Message: "web-2 /tmp/x was not in the savepoint: it came back empty"},
		{Code: model.NoticeCodeLayerNotRestored,
			Message: "web-1's writable layer was not in the savepoint: it started fresh from its image"},
	}, notices, "a mount nothing excluded, a tmpfs, holds nothing to restore")
}

func TestNewSavepointSummary_Replicas(t *testing.T) {
	// Arrange
	detail := SavepointDetail{WritableLayers: []WritableLayer{{Service: "db", Replica: 1}, {Service: "db", Replica: 3}}}

	// Act
	got := NewSavepointSummary(detail).Replicas

	// Assert
	assert.Equal(t, []model.ReplicaHolding{
		{Service: "db", Replica: 1, Held: []string{model.ReplicaHeldLayer}},
		{Service: "db", Replica: 3, Held: []string{model.ReplicaHeldLayer}},
	}, got)
}

// A record carries the header, the provider and the body, and no summary; it reads back whole.
func TestNewSavepointRecord(t *testing.T) {
	// Arrange
	savepoint := Savepoint{
		Header: model.Savepoint{ID: "019e843e-1650-7000-8000-000000000000", Name: "sp"},
		Detail: SavepointDetail{
			Stats: []SnapshotStat{{SizeBytes: 10}},
			Spec:  &SpecRef{Format: SpecFormatComposeYAML},
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

func TestParseSavepoint_Refuses(t *testing.T) {
	tests := []struct {
		name   string
		record model.Savepoint
	}{
		{name: "another provider's", record: model.Savepoint{ID: "sp", Provider: "kubernetes"}},
		{name: "no provider", record: model.Savepoint{ID: "sp"}},
		{name: "a body that does not decode", record: model.Savepoint{ID: "sp", Provider: ProviderName, Detail: json.RawMessage(`[]`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := ParseSavepoint(tt.record)

			// Assert
			require.ErrorIs(t, err, ErrSavepointDetail)
			assert.Zero(t, got)
		})
	}
}
