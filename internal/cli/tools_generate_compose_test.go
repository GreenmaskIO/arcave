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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

var generatedCompose = model.GenerateComposeResponse{
	ComposeProject: "demo",
	Services:       []string{"db", "web"},
	Spec:           []byte("name: demo\n"),
}

func TestToolsGenerateComposeHandler_GenerateCompose(t *testing.T) {
	tests := []struct {
		name     string
		existing fs.FileMode // the mode of a file already there, 0 for none
		force    bool
		wantMode fs.FileMode
	}{
		{name: "a new file is private to its owner", wantMode: 0o600},
		{name: "an existing file keeps its mode", existing: 0o640, force: true, wantMode: 0o640},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			dir := t.TempDir()
			path := filepath.Join(dir, "compose.yaml")
			if tt.existing != 0 {
				require.NoError(t, os.WriteFile(path, []byte("old\n"), tt.existing))
				require.NoError(t, os.Chmod(path, tt.existing))
			}
			svc := &mockService{}
			svc.On("GenerateCompose", mock.Anything, model.GenerateComposeRequest{Dir: dir}).Return(generatedCompose, nil).Once()
			printer := &capturePrinter{}
			printer.On("Print", mock.Anything).Return(nil).Once()
			h := NewToolsGenerateComposeHandler(svc, printer)

			// Act
			err := h.GenerateCompose(context.Background(), model.GenerateComposeRequest{}, path, tt.force)

			// Assert
			require.NoError(t, err)
			written, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "name: demo\n", string(written))
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMode, info.Mode().Perm())
			want := generatedCompose
			want.Path = path
			assert.Equal(t, []any{want}, printer.printed())
			svc.AssertExpectations(t)
			printer.AssertExpectations(t)
		})
	}
}

// An existing file is refused before the daemon is read, and left as it was.
func TestToolsGenerateComposeHandler_GenerateCompose_ExistingFile(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "compose.yaml")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))
	svc := &mockService{}
	printer := &capturePrinter{}
	h := NewToolsGenerateComposeHandler(svc, printer)

	// Act
	err := h.GenerateCompose(context.Background(), model.GenerateComposeRequest{}, path, false)

	// Assert
	require.ErrorContains(t, err, "pass --force")
	written, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "old\n", string(written))
	svc.AssertExpectations(t)
	printer.AssertExpectations(t)
}

// A forced write through a symlink replaces the file it points to and keeps the link.
func TestToolsGenerateComposeHandler_GenerateCompose_Symlink(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-compose.yaml")
	require.NoError(t, os.WriteFile(target, []byte("old\n"), 0o600))
	link := filepath.Join(dir, "compose.yaml")
	require.NoError(t, os.Symlink(target, link))
	svc := &mockService{}
	svc.On("GenerateCompose", mock.Anything, model.GenerateComposeRequest{Dir: dir}).Return(generatedCompose, nil).Once()
	printer := &capturePrinter{}
	printer.On("Print", mock.Anything).Return(nil).Once()
	h := NewToolsGenerateComposeHandler(svc, printer)

	// Act
	err := h.GenerateCompose(context.Background(), model.GenerateComposeRequest{}, link, true)

	// Assert
	require.NoError(t, err)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&fs.ModeSymlink, "the link stays a link")
	written, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "name: demo\n", string(written))
	svc.AssertExpectations(t)
}

// The fix names the CLI's flag; the service reports the domain condition only.
func TestToolsGenerateComposeHandler_GenerateCompose_ComposeProjectAbsent(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	svc := &mockService{}
	svc.On("GenerateCompose", mock.Anything, model.GenerateComposeRequest{Dir: dir}).
		Return(model.GenerateComposeResponse{}, fmt.Errorf("compose project %q: %w", "demo", model.ErrComposeProjectAbsent)).Once()
	h := NewToolsGenerateComposeHandler(svc, &capturePrinter{})

	// Act
	err := h.GenerateCompose(context.Background(), model.GenerateComposeRequest{}, filepath.Join(dir, "compose.yaml"), false)

	// Assert
	require.ErrorIs(t, err, model.ErrComposeProjectAbsent)
	assert.ErrorContains(t, err, "name another project with -p")
	svc.AssertExpectations(t)
}
