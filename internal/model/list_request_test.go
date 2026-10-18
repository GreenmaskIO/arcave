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

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestParseSortOrder(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		want    model.SortOrder
		wantErr error
	}{
		{name: "empty is newest first", token: "", want: model.SortDesc},
		{name: "desc", token: "desc", want: model.SortDesc},
		{name: "asc", token: "asc", want: model.SortAsc},
		{name: "case matters", token: "ASC", wantErr: model.ErrInvalidSortOrder},
		{name: "anything else is refused", token: "newest", wantErr: model.ErrInvalidSortOrder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := model.ParseSortOrder(tt.token)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
