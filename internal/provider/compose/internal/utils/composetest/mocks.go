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

package composetest

import (
	"context"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

const DemoVolumeName = "demo_db"

var (
	DemoSpec      = &types.Project{Name: "demo"}
	DemoSpecBytes = []byte("name: demo\n")
)

// DemoVolume is the unit, in the form the mock tests address it by.
func DemoVolume() model.Volume {
	return model.Volume{
		ObjectID: "volume-named-" + DemoVolumeName,
		Backend:  model.SnapshotBackendFullCopy,
		Named:    &model.VolumePropertiesNamed{VolumeName: DemoVolumeName},
	}
}

// MockInspector doubles composeIntrospector.
type MockInspector struct{ mock.Mock }

func (m *MockInspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	args := m.Called(ctx)
	introspection, _ := args.Get(0).(model.IntrospectionResult)
	return introspection, args.Error(1)
}

// MockVolumeRemover doubles volumeRemover.
type MockVolumeRemover struct{ mock.Mock }

func (m *MockVolumeRemover) RemoveVolume(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// MockContainerRebuilder doubles containerRebuilder.
type MockContainerRebuilder struct{ mock.Mock }

func (m *MockContainerRebuilder) RecreateWithImage(
	ctx context.Context, containerID string, as model.ReplicaIdentity, imageRef string,
) (string, error) {
	args := m.Called(ctx, containerID, as, imageRef)
	return args.String(0), args.Error(1)
}

// MockImageLookup doubles imageLookup.
type MockImageLookup struct{ mock.Mock }

func (m *MockImageLookup) ImageRefExists(ctx context.Context, ref string) (bool, error) {
	args := m.Called(ctx, ref)
	return args.Bool(0), args.Error(1)
}

type MockAssembler struct{ mock.Mock }

func (m *MockAssembler) Assemble(ctx context.Context, introspection model.IntrospectionResult) (*types.Project, error) {
	args := m.Called(ctx, introspection)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}

type MockCodec struct{ mock.Mock }

func (m *MockCodec) Marshal(project *types.Project) ([]byte, error) {
	args := m.Called(project)
	spec, _ := args.Get(0).([]byte)
	return spec, args.Error(1)
}

func (m *MockCodec) Parse(ctx context.Context, spec []byte) (*types.Project, error) {
	args := m.Called(ctx, spec)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}

type MockRegistry struct{ mock.Mock }

func (m *MockRegistry) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	args := m.Called(spec)
	backend, _ := args.Get(0).(model.VolumeSnapshotBackend)
	return backend, args.Error(1)
}

// MatchSpec matches a backend built for objectID over store.
func MatchSpec(store storages.Storager, objectID string) any {
	return mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Store == store && spec.Volume.ObjectID == objectID
	})
}

// ExpectSpecCommit declares the artifact a capture writes before its record, under an id
// off the clock that passes the gate.
func ExpectSpecCommit(repo *MockSavepointRepo) *mock.Call {
	validID := mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil })
	return repo.On("SaveSpec", mock.Anything, validID, DemoSpecBytes).Return(nil).Once()
}
