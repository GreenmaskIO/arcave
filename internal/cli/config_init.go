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

// Package cli adapts arcave's services to Cobra commands: each handler invokes a
// service method and renders the result through a printer — the model types go
// out as JSON (Savepoint for single-savepoint payloads, the response envelopes
// for multi-part responses). The printer is injected, so handlers are unit-
// testable without touching stdout. Command definitions and flags live in
// cmd/arcave, which resolves the config and logger before RunE and passes typed
// arguments here — the handlers know nothing of cobra or viper.
package cli

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/config"
)

type configInitService interface {
	Init(ctx context.Context, cfg config.Config, override bool) (resolvedName, path string, err error)
}

type ConfigInitHandler struct {
	svc     configInitService
	printer printer
}

func NewConfigInitHandler(svc configInitService, printer printer) *ConfigInitHandler {
	return &ConfigInitHandler{svc: svc, printer: printer}
}

func (h *ConfigInitHandler) Init(ctx context.Context, cfg config.Config, override bool) error {
	resolvedName, path, err := h.svc.Init(ctx, cfg, override)
	if err != nil {
		return err
	}
	log.Ctx(ctx).Info().Str("name", resolvedName).
		Str("path", path).
		Msg("initialized arcave project")
	return nil
}
