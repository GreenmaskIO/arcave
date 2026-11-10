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

type sessionShowService interface {
	ShowSession(ctx context.Context, req model.ShowSessionRequest) (model.SessionDetail, error)
}

type SessionShowHandler struct {
	svc     sessionShowService
	printer printer
}

func NewSessionShowHandler(svc sessionShowService, printer printer) *SessionShowHandler {
	return &SessionShowHandler{svc: svc, printer: printer}
}

// SessionShow prints a session and its savepoints by compose project.
func (h *SessionShowHandler) SessionShow(ctx context.Context, req model.ShowSessionRequest) error {
	detail, err := h.svc.ShowSession(ctx, req)
	if err != nil {
		return err
	}
	return h.printer.Print(detail)
}
