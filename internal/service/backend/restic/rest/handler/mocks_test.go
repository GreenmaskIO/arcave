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

package handler

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

var _ model.RepositoryResolver = stubResolver{}

// stubResolver serves one repository under one name. A stub and not a mock:
// what a name resolves to is the resolver's question, not this package's.
type stubResolver struct {
	name string
	repo model.Repository
}

func (s stubResolver) Lookup(arcaveObjectID string) (model.Repository, error) {
	if arcaveObjectID != s.name {
		return nil, model.ErrRepositoryNotFound
	}
	return s.repo, nil
}

// failingResolver reports a failure that is not the sentinel: nothing the
// protocol has a status for.
type failingResolver struct{}

func (failingResolver) Lookup(string) (model.Repository, error) { return nil, errStore }

var _ model.Repository = (*mockRepository)(nil)

// mockRepository is the seam's double: which method a test asks of a repository,
// with which arguments, in which order, and what it answered.
type mockRepository struct {
	mock.Mock
}

func (m *mockRepository) Create(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockRepository) StatConfig(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockRepository) OpenConfig(ctx context.Context) (io.ReadCloser, error) {
	args := m.Called(ctx)
	body, _ := args.Get(0).(io.ReadCloser)
	return body, args.Error(1)
}

func (m *mockRepository) SaveConfig(ctx context.Context, body io.Reader) error {
	return m.Called(ctx, body).Error(0)
}

func (m *mockRepository) DeleteConfig(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func (m *mockRepository) ListBlobs(ctx context.Context, req model.ListBlobsRequest) ([]model.Blob, error) {
	args := m.Called(ctx, req)
	blobs, _ := args.Get(0).([]model.Blob)
	return blobs, args.Error(1)
}

func (m *mockRepository) StatBlob(ctx context.Context, req model.StatBlobRequest) (int64, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockRepository) OpenBlobRange(ctx context.Context, req model.OpenBlobRangeRequest) (io.ReadCloser, error) {
	args := m.Called(ctx, req)
	body, _ := args.Get(0).(io.ReadCloser)
	return body, args.Error(1)
}

func (m *mockRepository) SaveBlob(ctx context.Context, req model.SaveBlobRequest) error {
	return m.Called(ctx, req).Error(0)
}

func (m *mockRepository) DeleteBlob(ctx context.Context, req model.DeleteBlobRequest) error {
	return m.Called(ctx, req).Error(0)
}
