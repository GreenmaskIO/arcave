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

package composetools

import (
	"context"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
)

// demoProject is the compose project every test generates a file of.
const demoProject = "demo"

// demoIntrospection is the live compose project: one service mounting one named volume.
func demoIntrospection() model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: "db",
			Volumes: []model.ServiceVolume{{
				Type: model.VolumeTypeNamed, Source: "db", Target: "/data", VolumeName: "demo_db",
			}},
		}},
		Volumes: []model.NamedVolume{{Key: "db", Name: "demo_db",
			UsedBy: []model.ServiceReference{{Service: "db", Target: "/data"}}}},
	}}
}

// demoAbsentIntrospection is what the inspector reports once the compose project was taken down:
// no service, its named volume left behind.
func demoAbsentIntrospection() model.IntrospectionResult {
	introspection := demoIntrospection()
	introspection.Project.Services = nil
	introspection.Project.Volumes[0].UsedBy = nil
	return introspection
}

type mockInspector struct{ mock.Mock }

func (m *mockInspector) Introspect(ctx context.Context) (model.IntrospectionResult, error) {
	args := m.Called(ctx)
	introspection, _ := args.Get(0).(model.IntrospectionResult)
	return introspection, args.Error(1)
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
