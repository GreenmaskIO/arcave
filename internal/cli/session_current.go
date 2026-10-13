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

type sessionCurrentService interface {
	ResolveSession(ctx context.Context, ref string) (model.Session, error)
}

type SessionCurrentHandler struct {
	svc     sessionCurrentService
	printer printer
}

func NewSessionCurrentHandler(svc sessionCurrentService, printer printer) *SessionCurrentHandler {
	return &SessionCurrentHandler{svc: svc, printer: printer}
}

// SessionCurrent prints the session a command works in: ref when set, else the current session,
// else the default.
func (h *SessionCurrentHandler) SessionCurrent(ctx context.Context, ref string) error {
	session, err := h.svc.ResolveSession(ctx, ref)
	if err != nil {
		return err
	}
	return h.printer.Print(session)
}
