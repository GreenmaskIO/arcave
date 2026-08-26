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

type savepointRemoveService interface {
	RemoveSavepoints(ctx context.Context, req model.RemoveRequest) (model.RemoveResult, error)
}

// SavepointRemoveHandler holds the services the CLI adapter delegates to and the printer that
// renders their results.
type SavepointRemoveHandler struct {
	svc     savepointRemoveService
	printer printer
}

func NewSavepointRemoveHandler(svc savepointRemoveService, printer printer) *SavepointRemoveHandler {
	return &SavepointRemoveHandler{
		svc:     svc,
		printer: printer,
	}
}

// SavepointRemove removes savepoints and prints what was (or would be) removed.
func (h *SavepointRemoveHandler) SavepointRemove(ctx context.Context, req model.RemoveRequest) error {
	res, err := h.svc.RemoveSavepoints(ctx, req)
	if err != nil {
		return removeUsageError(err)
	}
	return h.printer.Print(model.RemoveResponse(res))
}
