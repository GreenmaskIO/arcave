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

type sessionListService interface {
	ListSessions(ctx context.Context) ([]model.SessionSummary, error)
}

type SessionListHandler struct {
	svc     sessionListService
	printer printer
}

func NewSessionListHandler(svc sessionListService, printer printer) *SessionListHandler {
	return &SessionListHandler{svc: svc, printer: printer}
}

// SessionList prints every session of the arcave project as a JSON array, the default first.
func (h *SessionListHandler) SessionList(ctx context.Context) error {
	sessions, err := h.svc.ListSessions(ctx)
	if err != nil {
		return err
	}
	return h.printer.Print(sessions)
}
