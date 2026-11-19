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
	"errors"
	"fmt"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// ErrRecordNotFound refuses a savepoint id the store holds no committed record of.
var ErrRecordNotFound = errors.New("savepoint not found")

// SavepointLoader reads the committed records of ids; one not found is left out.
type SavepointLoader interface {
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]composemodel.Savepoint, error)
}

// LoadSavepoint reads the record of the savepoint id names.
func LoadSavepoint(ctx context.Context, loader SavepointLoader, id model.SavepointID) (composemodel.Savepoint, error) {
	savepoints, err := loader.LoadSavepoints(ctx, []model.SavepointID{id})
	if err != nil {
		return composemodel.Savepoint{}, err
	}
	if len(savepoints) == 0 {
		return composemodel.Savepoint{}, fmt.Errorf("%w: %s", ErrRecordNotFound, id)
	}
	return savepoints[0], nil
}
