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

package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

func TestSavepoints_Load(t *testing.T) {
	id := model.SavepointID("019e843e-1650-7000-8000-000000000001")
	errRead := errors.New("read failed")
	tests := []struct {
		name      string
		loaded    []composemodel.Savepoint
		loadErr   error
		want      model.Savepoint
		wantFound bool
		wantErr   error
	}{
		{
			name: "committed, summed up",
			loaded: []composemodel.Savepoint{{
				Header: model.Savepoint{ID: id},
				Detail: composemodel.SavepointDetail{Stats: []composemodel.SnapshotStat{{SizeBytes: 10, StoredBytes: 4}}},
			}},
			want: model.Savepoint{
				ID:       id,
				Provider: composemodel.ProviderName,
				Summary: model.SavepointSummary{
					Units: []model.UnitTotal{{Kind: model.UnitKindVolumes, Count: 1, SizeBytes: 10, StoredBytes: 4}},
				},
				Detail: json.RawMessage(`{"stats":[{"volume":{"object_id":"","compose_project":"","type":"","backend":""},"type":"","size_bytes":10,"stored_bytes":4}]}`),
			},
			wantFound: true,
		},
		{name: "never committed or removed since"},
		{name: "a record that cannot be read", loadErr: errRead, wantErr: errRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			records := &composetest.MockSavepointRepo{}
			records.On("LoadSavepoints", mock.Anything, []model.SavepointID{id}).Return(tt.loaded, tt.loadErr).Once()

			// Act
			got, found, err := NewSavepoints(records).Load(context.Background(), id)

			// Assert
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.want, got)
			records.AssertExpectations(t)
		})
	}
}
