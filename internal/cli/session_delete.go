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

type sessionDeleteService interface {
	DeleteSession(ctx context.Context, req model.DeleteSessionRequest) (model.DeleteSessionResponse, error)
}

type SessionDeleteHandler struct {
	svc     sessionDeleteService
	printer printer
}

func NewSessionDeleteHandler(svc sessionDeleteService, printer printer) *SessionDeleteHandler {
	return &SessionDeleteHandler{svc: svc, printer: printer}
}

// SessionDelete deletes a session by id or name, or a recordless tree by id, and prints which
// one went and the bytes it freed.
func (h *SessionDeleteHandler) SessionDelete(ctx context.Context, req model.DeleteSessionRequest) error {
	resp, err := h.svc.DeleteSession(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(resp)
}
