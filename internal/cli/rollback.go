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
	"time"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
)

type rollbackService interface {
	Rollback(ctx context.Context, req model.RollbackRequest) (model.RollbackResult, error)
}

type RollbackHandler struct {
	svc     rollbackService
	printer printer
}

func NewRollbackHandler(svc rollbackService, printer printer) *RollbackHandler {
	return &RollbackHandler{
		svc:     svc,
		printer: printer,
	}
}

// Rollback restores a savepoint and prints what was restored; the wait bound and the
// leftover-volumes policy come from cfg.
func (h *RollbackHandler) Rollback(ctx context.Context, cfg config.Config, ref string) error {
	res, err := h.svc.Rollback(ctx, newRollbackRequest(ref, cfg.Compose.WaitTimeout, cfg.Rollback.Policy()))
	if err != nil {
		return err
	}
	return h.printer.Print(newRollbackResponse(res))
}

// newRollbackRequest builds the request both adapters send, so neither drifts on
// what a ref means: "latest" is reserved, a canonical UUID is an id, the rest is a name.
func newRollbackRequest(
	ref string, waitTimeout time.Duration, leftoverVolumesPolicy model.LeftoverVolumesPolicy,
) model.RollbackRequest {
	req := model.RollbackRequest{
		WaitTimeout:     waitTimeout,
		LeftoverVolumes: leftoverVolumesPolicy,
	}
	switch {
	case ref == model.RefLatest:
		latest := true
		req.Latest = &latest
	case model.SavepointID(ref).Validate() == nil:
		id := model.SavepointID(ref)
		req.ID = &id
	default:
		req.Name = &ref
	}
	return req
}

// newRollbackResponse builds the response both adapters return, so neither drops a field.
func newRollbackResponse(res model.RollbackResult) model.RollbackResponse {
	return model.RollbackResponse{
		RolledBackTo:       res.Savepoint,
		Restored:           res.Restored,
		WritableLayers:     res.WritableLayers,
		SafetySnapshotKept: res.SafetySnapshotKept,
	}
}
