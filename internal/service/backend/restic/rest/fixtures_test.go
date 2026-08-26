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

package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// testID is a name of the shape restic writes: 64 lowercase hex characters, and
// stubBody is what the repository below holds under it.
var (
	testID   = model.ObjectID(strings.Repeat("0a1b2c3d", 8))
	stubBody = []byte("0123456789abcdef")
)

// The names the route table addresses. A name the resolver does not serve is a
// 404 before any operation runs, so every name a case expects an answer from has
// to be one of the first three.
const (
	testRepoName  = "testrepo"
	otherRepoName = "other"
	// objectIDName is a real arcave ObjectID: one segment, with the mount path
	// flattened into it — the shape a name on this wire actually has.
	objectIDName    = "volume-service-app-var-lib-data"
	unknownRepoName = "nosuchrepo"
)

// errStore is a failure that is none of the protocol's own conditions.
var errStore = errors.New("store unreachable")

// journalled carries a logger the way a command's context does — the only way
// this package's own lines can be read back.
func journalled(w io.Writer) context.Context {
	logger := zerolog.New(w).Level(zerolog.DebugLevel)
	return logger.WithContext(context.Background())
}

// serving is the resolver these tests reach a repository through. A double
// rather than repo.NewResolver over a real store: what this package's tests are
// about is routing, and a name that resolves to nothing has to be a 404 without
// a filesystem in the way.
func serving(repos map[string]model.Repository) model.RepositoryResolver {
	return stubResolver(repos)
}

// oneRepository serves repo under every name the route table addresses, so a
// case about routing says nothing about resolution.
func oneRepository(repo model.Repository) model.RepositoryResolver {
	return serving(map[string]model.Repository{
		testRepoName:  repo,
		otherRepoName: repo,
		objectIDName:  repo,
	})
}

var _ model.RepositoryResolver = stubResolver(nil)

// stubResolver answers with the repository a name is mapped to, and
// ErrRepositoryNotFound for every other name.
type stubResolver map[string]model.Repository

func (s stubResolver) Lookup(arcaveObjectID string) (model.Repository, error) {
	repo, ok := s[arcaveObjectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", model.ErrRepositoryNotFound, arcaveObjectID)
	}
	return repo, nil
}

var _ model.Repository = stubRepository{}

// stubRepository answers every operation successfully, for the tests about
// routing rather than about the protocol's own answers. Its body is what a read
// returns; the zero value serves stubBody.
type stubRepository struct{ body []byte }

func (s stubRepository) content() []byte {
	if s.body == nil {
		return stubBody
	}
	return s.body
}

func (stubRepository) Create(context.Context) error { return nil }

func (s stubRepository) StatConfig(context.Context) (int64, error) {
	return int64(len(s.content())), nil
}

func (s stubRepository) OpenConfig(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.content())), nil
}

func (stubRepository) SaveConfig(context.Context, io.Reader) error { return nil }

func (stubRepository) DeleteConfig(context.Context) error { return nil }

func (s stubRepository) ListBlobs(context.Context, model.ListBlobsRequest) ([]model.Blob, error) {
	return []model.Blob{{Name: string(testID), Size: int64(len(s.content()))}}, nil
}

func (s stubRepository) StatBlob(context.Context, model.StatBlobRequest) (int64, error) {
	return int64(len(s.content())), nil
}

func (s stubRepository) OpenBlobRange(_ context.Context, req model.OpenBlobRangeRequest) (io.ReadCloser, error) {
	body := s.content()
	if req.Offset+req.Length > int64(len(body)) {
		return nil, fmt.Errorf("the range %d+%d is outside the stub", req.Offset, req.Length)
	}
	return io.NopCloser(bytes.NewReader(body[req.Offset : req.Offset+req.Length])), nil
}

func (stubRepository) SaveBlob(context.Context, model.SaveBlobRequest) error {
	return nil
}

func (stubRepository) DeleteBlob(context.Context, model.DeleteBlobRequest) error {
	return nil
}

// failingRepository answers one operation with a condition the protocol has no
// status for; everything else it inherits.
type failingRepository struct{ stubRepository }

func (failingRepository) OpenConfig(context.Context) (io.ReadCloser, error) { return nil, errStore }

// panickingRepository is the other way an operation can fail to answer.
type panickingRepository struct{ stubRepository }

func (panickingRepository) OpenConfig(context.Context) (io.ReadCloser, error) { panic("boom") }
