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

package imagecopy

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

type mockContainerImager struct{ mock.Mock }

func (m *mockContainerImager) CommitContainer(
	ctx context.Context, containerID, comment string, labels map[string]string,
) (model.ImageDigestID, error) {
	args := m.Called(ctx, containerID, comment, labels)
	return args.Get(0).(model.ImageDigestID), args.Error(1)
}

func (m *mockContainerImager) SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error) {
	args := m.Called(ctx, imageID)
	rc, _ := args.Get(0).(io.ReadCloser)
	return rc, args.Error(1)
}

func (m *mockContainerImager) RemoveImage(ctx context.Context, imageID model.ImageDigestID) error {
	return m.Called(ctx, imageID).Error(0)
}

// LoadImage mirrors the daemon: it reports a failed load in-band and answers 200,
// so a stream that broke mid-read is not what the caller hears about.
func (m *mockContainerImager) LoadImage(ctx context.Context, r io.Reader) error {
	archive, _ := io.ReadAll(r)
	return m.Called(ctx, string(archive)).Error(0)
}

func (m *mockContainerImager) TagImage(ctx context.Context, imageID model.ImageDigestID, ref string) error {
	return m.Called(ctx, imageID, ref).Error(0)
}

func (m *mockContainerImager) ImageExists(ctx context.Context, imageID model.ImageDigestID) (bool, error) {
	args := m.Called(ctx, imageID)
	return args.Bool(0), args.Error(1)
}
