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

package compose

import (
	"context"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

const demoVolumeName = "demo_db"

var (
	demoSpec      = &types.Project{Name: "demo"}
	demoSpecBytes = []byte("name: demo\n")
)

// demoVolume is the unit, in the form the mock tests address it by.
func demoVolume() model.Volume {
	return model.Volume{
		ObjectID: "volume-named-" + demoVolumeName,
		Backend:  model.SnapshotBackendFullCopy,
		Named:    &model.VolumePropertiesNamed{VolumeName: demoVolumeName},
	}
}

// mockInspector doubles composeIntrospector.
type mockInspector struct{ mock.Mock }

func (m *mockInspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	args := m.Called(ctx)
	introspection, _ := args.Get(0).(model.IntrospectionResult)
	return introspection, args.Error(1)
}

// mockVolumeRemover doubles volumeRemover.
type mockVolumeRemover struct{ mock.Mock }

func (m *mockVolumeRemover) RemoveVolume(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// mockContainerRebuilder doubles containerRebuilder.
type mockContainerRebuilder struct{ mock.Mock }

func (m *mockContainerRebuilder) RecreateWithImage(
	ctx context.Context, containerID string, as model.ReplicaIdentity, imageRef string,
) (string, error) {
	args := m.Called(ctx, containerID, as, imageRef)
	return args.String(0), args.Error(1)
}

// mockImageLookup doubles imageLookup.
type mockImageLookup struct{ mock.Mock }

func (m *mockImageLookup) ImageRefExists(ctx context.Context, ref string) (bool, error) {
	args := m.Called(ctx, ref)
	return args.Bool(0), args.Error(1)
}

type mockAssembler struct{ mock.Mock }

func (m *mockAssembler) Assemble(ctx context.Context, introspection model.IntrospectionResult) (*types.Project, error) {
	args := m.Called(ctx, introspection)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}

type mockCodec struct{ mock.Mock }

func (m *mockCodec) Marshal(project *types.Project) ([]byte, error) {
	args := m.Called(project)
	spec, _ := args.Get(0).([]byte)
	return spec, args.Error(1)
}

func (m *mockCodec) Parse(ctx context.Context, spec []byte) (*types.Project, error) {
	args := m.Called(ctx, spec)
	project, _ := args.Get(0).(*types.Project)
	return project, args.Error(1)
}

type mockRegistry struct{ mock.Mock }

func (m *mockRegistry) New(spec model.BackendSpec) (model.VolumeSnapshotBackend, error) {
	args := m.Called(spec)
	backend, _ := args.Get(0).(model.VolumeSnapshotBackend)
	return backend, args.Error(1)
}

// matchSpec matches a backend built for objectID over store.
func matchSpec(store storages.Storager, objectID string) any {
	return mock.MatchedBy(func(spec model.BackendSpec) bool {
		return spec.Store == store && spec.Volume.ObjectID == objectID
	})
}

// expectSpecCommit declares the artifact a capture writes before its record, under an id
// off the clock that passes the gate.
func expectSpecCommit(repo *composetest.MockSavepointRepo) *mock.Call {
	validID := mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil })
	return repo.On("SaveSpec", mock.Anything, validID, demoSpecBytes).Return(nil).Once()
}
