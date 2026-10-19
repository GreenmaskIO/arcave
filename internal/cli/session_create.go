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

type sessionCreateService interface {
	CreateSession(ctx context.Context, req model.CreateSessionRequest) (model.Session, error)
}

type SessionCreateHandler struct {
	svc     sessionCreateService
	printer printer
}

func NewSessionCreateHandler(svc sessionCreateService, printer printer) *SessionCreateHandler {
	return &SessionCreateHandler{svc: svc, printer: printer}
}

// SessionCreate creates a session, empty or a clone of req.From, and prints its record.
func (h *SessionCreateHandler) SessionCreate(ctx context.Context, req model.CreateSessionRequest) error {
	session, err := h.svc.CreateSession(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(session)
}
