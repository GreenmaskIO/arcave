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
	"slices"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/greenmaskio/storages"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

const DemoVolumeName = "demo_db"

var (
	DemoSpec      = &types.Project{Name: "demo"}
	DemoSpecBytes = []byte("name: demo\n")
)

// DemoVolume is the unit, in the form the mock tests address it by.
func DemoVolume() composemodel.Volume {
	return composemodel.Volume{
		ObjectID: "volume-named-" + DemoVolumeName,
		Backend:  composeconfig.SnapshotBackendFullCopy,
		Named:    &composemodel.VolumePropertiesNamed{VolumeName: DemoVolumeName},
	}
}

// MockInspector doubles composeIntrospector.
type MockInspector struct{ mock.Mock }

func (m *MockInspector) Introspect(ctx context.Context) (composemodel.IntrospectionResult, error) {
	args := m.Called(ctx)
	introspection, _ := args.Get(0).(composemodel.IntrospectionResult)
	return introspection, args.Error(1)
}

func (m *MockInspector) ReadResources(ctx context.Context) (composemodel.ComposeProjectResources, error) {
	args := m.Called(ctx)
	resources, _ := args.Get(0).(composemodel.ComposeProjectResources)
	return resources, args.Error(1)
}

// ResourcesOfVolumes is what the narrow read reports of introspection's owned volumes, sorted.
func ResourcesOfVolumes(introspection composemodel.IntrospectionResult) composemodel.ComposeProjectResources {
	var names []string
	for _, v := range introspection.Project.Volumes {
		if !v.External {
			names = append(names, v.Name)
		}
	}
	slices.Sort(names)
	return composemodel.ComposeProjectResources{Volumes: names}
}

// MockVolumeRemover doubles volumeRemover.
type MockVolumeRemover struct{ mock.Mock }

func (m *MockVolumeRemover) RemoveVolume(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// MockProjectRemover doubles projectRemover.
type MockProjectRemover struct{ mock.Mock }

func (m *MockProjectRemover) RemoveContainer(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

func (m *MockProjectRemover) RemoveNetwork(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// MockLayerTags doubles layerTags.
type MockLayerTags struct{ mock.Mock }

func (m *MockLayerTags) ListLayerImages(ctx context.Context) ([]composemodel.TaggedImage, error) {
	args := m.Called(ctx)
	images, _ := args.Get(0).([]composemodel.TaggedImage)
	return images, args.Error(1)
}

func (m *MockLayerTags) RemoveImageRef(ctx context.Context, ref model.ImageRef) error {
	return m.Called(ctx, ref).Error(0)
}

// MockContainerRebuilder doubles containerRebuilder.
type MockContainerRebuilder struct{ mock.Mock }

func (m *MockContainerRebuilder) RecreateWithImage(
	ctx context.Context, containerID string, as composemodel.ReplicaIdentity, imageRef string,
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

func (m *MockAssembler) Assemble(ctx context.Context, introspection composemodel.IntrospectionResult) (*types.Project, error) {
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

func (m *MockRegistry) New(spec composemodel.BackendSpec) (composemodel.VolumeSnapshotBackend, error) {
	args := m.Called(spec)
	backend, _ := args.Get(0).(composemodel.VolumeSnapshotBackend)
	return backend, args.Error(1)
}

// MatchSpec matches a backend built for objectID over store.
func MatchSpec(store storages.Storager, objectID string) any {
	return mock.MatchedBy(func(spec composemodel.BackendSpec) bool {
		return spec.Store == store && spec.Volume.ObjectID == objectID
	})
}

// ExpectSpecCommit declares the artifact a capture writes before its record, under an id
// off the clock that passes the gate.
func ExpectSpecCommit(repo *MockSavepointRepo) *mock.Call {
	validID := mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil })
	return repo.On("SaveSpec", mock.Anything, validID, DemoSpecBytes).Return(nil).Once()
}
