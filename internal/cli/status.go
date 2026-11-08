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
	"errors"

	"github.com/greenmaskio/arcave/internal/model"
)

type statusService interface {
	Status(ctx context.Context, req model.StatusRequest) (model.ComposeProjectStatus, error)
}

type StatusHandler struct {
	svc     statusService
	printer printer
}

func NewStatusHandler(svc statusService, printer printer) *StatusHandler {
	return &StatusHandler{
		svc:     svc,
		printer: printer,
	}
}

// Status prints what the daemon runs for the compose project; a partial status prints it and still
// fails.
func (h *StatusHandler) Status(ctx context.Context, req model.StatusRequest) error {
	status, err := h.svc.Status(ctx, req)
	if err != nil && !errors.Is(err, model.ErrStatusPartial) {
		return err
	}
	if printErr := h.printer.Print(status); printErr != nil {
		return printErr
	}
	return err
}
