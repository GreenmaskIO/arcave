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

package cli

import (
	"context"

	"github.com/greenmaskio/arcave/internal/model"
)

type savepointListService interface {
	ListSavepoints(ctx context.Context, req model.ListRequest) ([]model.Savepoint, error)
}

type SavepointListHandler struct {
	svc     savepointListService
	printer printer
}

func NewSavepointListHandler(svc savepointListService, printer printer) *SavepointListHandler {
	return &SavepointListHandler{
		svc:     svc,
		printer: printer,
	}
}

// SavepointList prints the savepoints req asks for as a JSON array. It reads
// only the savepoint store, so it needs no config.
func (h *SavepointListHandler) SavepointList(ctx context.Context, req model.ListRequest) error {
	sps, err := h.svc.ListSavepoints(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(sps)
}
