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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/greenmaskio/arcave/internal/model"
)

// newFileMode is a new compose file's mode: it carries every environment value, secrets included.
const newFileMode fs.FileMode = 0o600

type toolsGenerateComposeService interface {
	GenerateCompose(ctx context.Context, req model.GenerateComposeRequest) (model.GenerateComposeResponse, error)
}

// ToolsGenerateComposeHandler serves 'arcave tools generate-compose'.
type ToolsGenerateComposeHandler struct {
	svc     toolsGenerateComposeService
	printer printer
}

func NewToolsGenerateComposeHandler(svc toolsGenerateComposeService, printer printer) *ToolsGenerateComposeHandler {
	return &ToolsGenerateComposeHandler{
		svc:     svc,
		printer: printer,
	}
}

// GenerateCompose writes the compose file to output and prints its summary as JSON. An existing
// output is refused unless force.
func (h *ToolsGenerateComposeHandler) GenerateCompose(
	ctx context.Context, req model.GenerateComposeRequest, output string, force bool,
) error {
	path, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve output %q: %w", output, err)
	}
	// Before the daemon is read: a refused file costs nothing.
	if !force {
		if err := requireNoFile(path); err != nil {
			return err
		}
	}
	// Bind sources are made relative to where the file lands, so it finds them from there.
	req.Dir = filepath.Dir(path)
	generated, err := h.svc.GenerateCompose(ctx, req)
	if err != nil {
		return composeProjectAbsentError(err, "-p")
	}
	// Through a symlink, and an existing file keeps its mode.
	if err := os.WriteFile(path, generated.Spec, newFileMode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	generated.Path = path
	return h.printer.Print(generated)
}

// requireNoFile refuses a path something already exists at.
func requireNoFile(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("%s already exists: pass --force to overwrite it", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return nil
}
