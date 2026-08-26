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

package fullcopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

type mockVolumeStreamer struct{ mock.Mock }

func (m *mockVolumeStreamer) ResolveMountSource(
	ctx context.Context, v model.Volume,
) (model.MountSource, error) {
	args := m.Called(ctx, v)
	src, _ := args.Get(0).(model.MountSource)
	return src, args.Error(1)
}

func (m *mockVolumeStreamer) OpenVolume(
	ctx context.Context, src model.MountSource,
) (io.ReadCloser, error) {
	args := m.Called(ctx, src)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockVolumeStreamer) WriteVolume(ctx context.Context, src model.MountSource, r io.Reader) error {
	return m.Called(ctx, src, r).Error(0)
}

// failingReader fails mid-stream; a mock cannot, since it only hands the reader over.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
