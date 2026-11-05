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

package podman

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// mockLibpod is a test double for libpod (the imageLibpod and containerLibpod seams).
type mockLibpod struct {
	mock.Mock
}

func (m *mockLibpod) inspectContainer(ctx context.Context, containerID string) (libpodContainer, error) {
	args := m.Called(ctx, containerID)
	return args.Get(0).(libpodContainer), args.Error(1)
}

func (m *mockLibpod) commitContainer(ctx context.Context, commit libpodCommit) (string, error) {
	args := m.Called(ctx, commit)
	return args.String(0), args.Error(1)
}

func (m *mockLibpod) exportImage(ctx context.Context, imageID, format string) (io.ReadCloser, error) {
	args := m.Called(ctx, imageID, format)
	archive, _ := args.Get(0).(io.ReadCloser)
	return archive, args.Error(1)
}

// mockOneShotRunner is a test double for the engine a volume export runs its container through.
type mockOneShotRunner struct {
	mock.Mock
}

func (m *mockOneShotRunner) RunOneShot(ctx context.Context, spec model.OneShotSpec) error {
	return m.Called(ctx, spec).Error(0)
}
