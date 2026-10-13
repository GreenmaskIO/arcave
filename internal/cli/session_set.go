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

type sessionSetService interface {
	SetSession(ctx context.Context, ref string) (model.Session, error)
}

type SessionSetHandler struct {
	svc     sessionSetService
	printer printer
}

func NewSessionSetHandler(svc sessionSetService, printer printer) *SessionSetHandler {
	return &SessionSetHandler{svc: svc, printer: printer}
}

// SessionSet makes the session ref names the current one and prints its record.
func (h *SessionSetHandler) SessionSet(ctx context.Context, ref string) error {
	session, err := h.svc.SetSession(ctx, ref)
	if err != nil {
		return err
	}
	return h.printer.Print(session)
}
