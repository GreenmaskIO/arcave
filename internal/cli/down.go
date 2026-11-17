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

type downService interface {
	Down(ctx context.Context, req model.DownRequest) (model.DownResult, error)
}

// DownHandler ends a compose project's environment on the daemon.
type DownHandler struct {
	svc     downService
	printer printer
}

func NewDownHandler(svc downService, printer printer) *DownHandler {
	return &DownHandler{
		svc:     svc,
		printer: printer,
	}
}

// Down takes the environment off the daemon and prints what went.
func (h *DownHandler) Down(ctx context.Context, force bool) error {
	res, err := h.svc.Down(ctx, model.DownRequest{Force: force})
	if err != nil {
		return deploymentError(err, cliDeploymentFixHints)
	}
	return h.printer.Print(res)
}
