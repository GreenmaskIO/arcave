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
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
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

type mockObserver struct{ mock.Mock }

func (m *mockObserver) Observe(ctx context.Context) (provider.ObservedWorkload, error) {
	args := m.Called(ctx)
	observed, _ := args.Get(0).(provider.ObservedWorkload)
	return observed, args.Error(1)
}

type mockObservedWorkload struct{ mock.Mock }

func (m *mockObservedWorkload) Deployment() *model.SavepointDeployment {
	args := m.Called()
	deployment, _ := args.Get(0).(*model.SavepointDeployment)
	return deployment
}

func (m *mockObservedWorkload) Report(checkedAt time.Time, deployed *model.Savepoint) model.WorkloadReport {
	args := m.Called(checkedAt, deployed)
	return args.Get(0).(model.WorkloadReport)
}

// fakeDetail is a provider's body of a status, its JSON body.
type fakeDetail struct {
	body string
}

func (f fakeDetail) MarshalJSON() ([]byte, error) { return []byte(f.body), nil }

func (f fakeDetail) Table() model.Table { return model.Table{} }

type mockSavepoints struct{ mock.Mock }

func (m *mockSavepoints) List(ctx context.Context) ([]model.Savepoint, error) {
	args := m.Called(ctx)
	savepoints, _ := args.Get(0).([]model.Savepoint)
	return savepoints, args.Error(1)
}

func (m *mockSavepoints) Load(ctx context.Context, id model.SavepointID) (model.Savepoint, bool, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(model.Savepoint), args.Bool(1), args.Error(2)
}

func (m *mockSavepoints) Remove(ctx context.Context, id model.SavepointID) error {
	return m.Called(ctx, id).Error(0)
}

// sessionOf is a provider session whose every workload's savepoints are savepoints.
type sessionOf struct {
	fakeSession
	savepoints provider.Savepoints
}

func (s sessionOf) Savepoints(provider.WorkloadName) (provider.Savepoints, error) {
	return s.savepoints, nil
}

// failingReader is a SessionReader whose every read fails with err.
type failingReader struct{ err error }

func (f failingReader) ListSessions(context.Context) ([]model.Session, error) { return nil, f.err }

// fakeSession is the provider's session id: in the default session demoProject holds savepoints,
// anything else none; openErr fails opening a workload's savepoints, err every read of one.
type fakeSession struct {
	id         model.SessionID
	savepoints []model.Savepoint
	openErr    error
	err        error
}

func newFakeSession(id model.SessionID, savepoints []model.Savepoint, openErr, err error) fakeSession {
	if id != model.DefaultSession {
		savepoints = nil
	}
	return fakeSession{
		id:         id,
		savepoints: savepoints,
		openErr:    openErr,
		err:        err,
	}
}

func (f fakeSession) ID() model.SessionID { return f.id }

func (f fakeSession) Workloads(context.Context) ([]provider.WorkloadName, error) { return nil, nil }

func (f fakeSession) Savepoints(workload provider.WorkloadName) (provider.Savepoints, error) {
	switch {
	case f.openErr != nil:
		return nil, f.openErr
	case workload != demoProject:
		return newFakeSession(f.id, nil, nil, f.err), nil
	default:
		return f, nil
	}
}

func (f fakeSession) GarbageDataKinds() []model.GarbageDataKind { return nil }

func (f fakeSession) FindGarbage(context.Context, []model.GarbageDataKind) []provider.Garbage {
	return nil
}

func (f fakeSession) List(context.Context) ([]model.Savepoint, error) { return f.savepoints, f.err }

func (f fakeSession) Load(_ context.Context, id model.SavepointID) (model.Savepoint, bool, error) {
	if f.err != nil {
		return model.Savepoint{}, false, f.err
	}
	for _, savepoint := range f.savepoints {
		if savepoint.ID == id {
			return savepoint, true, nil
		}
	}
	return model.Savepoint{}, false, nil
}

func (f fakeSession) Remove(context.Context, model.SavepointID) error { return f.err }
