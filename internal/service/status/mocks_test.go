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

package status

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// fakePointer is a current-session pointer naming id, none when nil, or failing with err.
type fakePointer struct {
	id  *model.SessionID
	err error // returned by Get when set
}

func (f *fakePointer) Get(_ context.Context) (model.SessionID, bool, error) {
	if f.err != nil {
		return model.SessionID{}, false, f.err
	}
	if f.id == nil {
		return model.SessionID{}, false, nil
	}
	return *f.id, true, nil
}

type mockInflight struct{ mock.Mock }

func (m *mockInflight) Get(ctx context.Context) (model.InflightMarker, bool, error) {
	args := m.Called(ctx)
	marker, _ := args.Get(0).(model.InflightMarker)
	return marker, args.Bool(1), args.Error(2)
}

type mockContainersReader struct{ mock.Mock }

func (m *mockContainersReader) ReadContainers(ctx context.Context) (model.ComposeProjectContainers, error) {
	args := m.Called(ctx)
	return args.Get(0).(model.ComposeProjectContainers), args.Error(1)
}

// failingReader is a SessionReader and a SavepointLoader whose every read fails with err.
type failingReader struct{ err error }

func (f failingReader) ListSessions(context.Context) ([]model.Session, error) { return nil, f.err }

func (f failingReader) LoadSavepoints(context.Context, []model.SavepointID) ([]model.Savepoint, error) {
	return nil, f.err
}
