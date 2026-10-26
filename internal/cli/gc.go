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
	"errors"

	"github.com/greenmaskio/arcave/internal/model"
)

type gcService interface {
	GarbageCollect(ctx context.Context, req model.GarbageCollectRequest) (model.GarbageCollectResult, error)
}

// ValidateGarbageCollectRequest refuses, before anything is opened, a request the service
// would refuse.
func ValidateGarbageCollectRequest(req model.GarbageCollectRequest) error {
	return req.Validate()
}

type GCHandler struct {
	svc     gcService
	printer printer
}

func NewGCHandler(svc gcService, printer printer) *GCHandler {
	return &GCHandler{
		svc:     svc,
		printer: printer,
	}
}

// GarbageCollect runs req and prints the report; a partial gc, one stopped midway through deleting
// included, prints it and still fails.
func (h *GCHandler) GarbageCollect(ctx context.Context, req model.GarbageCollectRequest) error {
	result, err := h.svc.GarbageCollect(ctx, req)
	if err != nil && !errors.Is(err, model.ErrGarbageCollectPartial) {
		return err
	}
	if printErr := h.printer.Print(result); printErr != nil {
		return printErr
	}
	return err
}
