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
	"cmp"
	"context"
	"time"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

type upService interface {
	Up(ctx context.Context, req model.UpRequest) (model.UpResult, error)
}

// UpHandler brings a compose project up from a savepoint, never over anything unless forced.
type UpHandler struct {
	svc     upService
	printer printer
}

func NewUpHandler(svc upService, printer printer) *UpHandler {
	return &UpHandler{
		svc:     svc,
		printer: printer,
	}
}

// Up builds the compose project from ref — the newest savepoint when empty — and prints what was
// built and, when forced over a present one, what was dropped; the wait bound comes from cfg.
func (h *UpHandler) Up(ctx context.Context, cfg config.Config, ref string, force bool) error {
	res, err := h.svc.Up(ctx, newUpRequest(ref, cfg.Compose.WaitTimeout, force))
	if err != nil {
		return deploymentError(err, cliDeploymentFixHints)
	}
	return h.printer.Print(res)
}

// newUpRequest builds the request both adapters send for an up: ref read as a rollback reads it,
// latest when empty.
func newUpRequest(ref string, waitTimeout time.Duration, force bool) model.UpRequest {
	selector := newRollbackRequest(cmp.Or(ref, model.RefLatest), waitTimeout, "", false)
	return model.UpRequest{
		ID:          selector.ID,
		Name:        selector.Name,
		Latest:      selector.Latest,
		WaitTimeout: waitTimeout,
		Force:       force,
	}
}
