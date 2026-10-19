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
	ListSessions(ctx context.Context, req model.ListSessionsRequest) ([]model.SessionSummary, error)
}

type SessionListHandler struct {
	svc     sessionListService
	printer printer
}

func NewSessionListHandler(svc sessionListService, printer printer) *SessionListHandler {
	return &SessionListHandler{svc: svc, printer: printer}
}

// SessionList prints the arcave project's sessions as a JSON array in req.Sort's order; req.All adds
// the trees without a record, each with its status.
func (h *SessionListHandler) SessionList(ctx context.Context, req model.ListSessionsRequest) error {
	sessions, err := h.svc.ListSessions(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(sessions)
}
