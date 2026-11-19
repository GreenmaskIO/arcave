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

// Package sessionstore is what the compose provider keeps of a session, reached with no daemon: its
// compose projects and their savepoints.
package sessionstore

import (
	"context"
	"errors"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// SavepointRecords lists, reads and deletes the committed savepoints of one compose project.
type SavepointRecords interface {
	ListSavepoints(ctx context.Context) ([]composemodel.Savepoint, error)
	// LoadSavepoints returns the committed records of ids; one not found is left out.
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]composemodel.Savepoint, error)
	// DeleteSavepoint uncommits the record, then deletes the tree.
	DeleteSavepoint(ctx context.Context, id model.SavepointID) error
}

// savepoints are the savepoints of one compose project, reached with no daemon.
type savepoints struct {
	records SavepointRecords
}

// NewSavepoints is the compose project's savepoints over records.
func NewSavepoints(records SavepointRecords) provider.Savepoints {
	return savepoints{records: records}
}

// List returns every committed savepoint, as arcave reads it.
func (s savepoints) List(ctx context.Context) ([]model.Savepoint, error) {
	records, err := s.records.ListSavepoints(ctx)
	if err != nil {
		return nil, err
	}
	shared := make([]model.Savepoint, 0, len(records))
	for _, record := range records {
		savepoint, err := composemodel.ToSharedSavepoint(record)
		if err != nil {
			return nil, err
		}
		shared = append(shared, savepoint)
	}
	return shared, nil
}

// Load returns the committed savepoint id names, if any.
func (s savepoints) Load(ctx context.Context, id model.SavepointID) (model.Savepoint, bool, error) {
	savepoint, err := LoadSavepoint(ctx, s.records, id)
	switch {
	case errors.Is(err, ErrRecordNotFound):
		return model.Savepoint{}, false, nil
	case err != nil:
		return model.Savepoint{}, false, err
	}
	shared, err := composemodel.ToSharedSavepoint(savepoint)
	if err != nil {
		return model.Savepoint{}, false, err
	}
	return shared, true, nil
}

// Remove uncommits the savepoint id names, then deletes its tree.
func (s savepoints) Remove(ctx context.Context, id model.SavepointID) error {
	return s.records.DeleteSavepoint(ctx, id)
}
