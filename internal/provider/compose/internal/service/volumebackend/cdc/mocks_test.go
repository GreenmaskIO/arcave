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

package cdc

import (
	"context"
	"fmt"
	"io"
	"sync"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"

	"github.com/stretchr/testify/mock"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

// mockRunner doubles VolumeRunner and records every spec it was handed: a
// OneShotSpec carries the stdio writers this package creates, so no literal can
// name one — the expectations match the argv, and the assertions read the
// recording.
type mockRunner struct {
	mock.Mock
	mu    sync.Mutex
	specs []composemodel.OneShotSpec
}

func (m *mockRunner) RunOneShot(ctx context.Context, spec composemodel.OneShotSpec) error {
	m.mu.Lock()
	m.specs = append(m.specs, spec)
	m.mu.Unlock()
	return m.Called(ctx, spec).Error(0)
}

func (m *mockRunner) ResolveMountSource(ctx context.Context, v composemodel.Volume) (composemodel.MountSource, error) {
	args := m.Called(ctx, v)
	src, _ := args.Get(0).(composemodel.MountSource)
	return src, args.Error(1)
}

func (m *mockRunner) WipeVolume(ctx context.Context, src composemodel.MountSource) error {
	return m.Called(ctx, src).Error(0)
}

// Specs returns the runs in the order they happened.
func (m *mockRunner) Specs() []composemodel.OneShotSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]composemodel.OneShotSpec(nil), m.specs...)
}

// matchRestic matches a one-shot run by its restic subcommand.
func matchRestic(verb string) any {
	return mock.MatchedBy(func(spec composemodel.OneShotSpec) bool {
		return len(spec.Cmd) > 0 && spec.Cmd[0] == verb
	})
}

// mockServer doubles RepositoryServer.
type mockServer struct {
	mock.Mock
}

func (m *mockServer) ServeRepository(ctx context.Context, objectID string, stream io.ReadWriteCloser) error {
	return m.Called(ctx, objectID, stream).Error(0)
}

// respondWith writes lines to a run's stdout, standing in for what restic prints
// on --json.
func respondWith(lines ...string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		spec := args.Get(1).(composemodel.OneShotSpec)
		for _, line := range lines {
			fmt.Fprintln(spec.Stdout, line)
		}
	}
}
