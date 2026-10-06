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

package composespec

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// linkedProject holds db with two replicas, and app, the service linking to it.
func linkedProject() model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{Name: "app", Containers: []model.Container{{ID: "app1", Name: "demo-app-1", Number: 1}}},
			{Name: "db", Containers: []model.Container{
				{ID: "db1", Name: "demo-db-1", Number: 1},
				{ID: "db2", Name: "demo-db-2", Number: 2},
			}},
		},
	}}
}

func TestAssembly_DeclaredLinks(t *testing.T) {
	tests := []struct {
		name         string
		entries      []string
		wantLinks    []string
		wantExternal []string
	}{
		{name: "none"},
		{
			name: "an aliased link, compose's own aliases dropped, one per target replica",
			entries: []string{
				"demo-db-1:database", "demo-db-1:db-1", "demo-db-1:demo-db-1",
				"demo-db-2:database", "demo-db-2:db-1", "demo-db-2:demo-db-2",
			},
			wantLinks: []string{"db:database"},
		},
		{
			name:      "an alias repeating the service is the bare service",
			entries:   []string{"demo-db-1:db", "demo-db-1:db-1", "demo-db-1:demo-db-1"},
			wantLinks: []string{"db"},
		},
		{
			name: "the default bridge's legacy form",
			entries: []string{
				"/demo-db-1:/demo-app-1/database", "/demo-db-1:/demo-app-1/db-1", "/demo-db-1:/demo-app-1/demo-db-1",
			},
			wantLinks: []string{"db:database"},
		},
		{
			name:         "a container outside the project is an external link",
			entries:      []string{"outside:ext", "/legacy:/demo-app-1/legacy"},
			wantExternal: []string{"legacy", "outside:ext"},
		},
		{
			name:         "a mix, repeated across networks",
			entries:      []string{"demo-db-1:database", "outside:ext", "demo-db-1:database", "demo-db-1:primary"},
			wantLinks:    []string{"db:database", "db:primary"},
			wantExternal: []string{"outside:ext"},
		},
		{
			name:    "an entry with no alias is skipped",
			entries: []string{"demo-db-1", "demo-db-1:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			links, external := newAssembly(linkedProject()).declaredLinks(tt.entries, 1)

			// Assert
			assert.Equal(t, tt.wantLinks, links)
			assert.Equal(t, tt.wantExternal, external)
		})
	}
}

// A link on a project network is reported on the endpoint, and a link implies the
// dependency compose's loader would add.
func TestServiceConfig_Links(t *testing.T) {
	// Arrange
	res := linkedProject()
	app := res.Project.Services[0]
	app.Containers[0].Inspect = container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: "app1", HostConfig: &container.HostConfig{}},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"demo_default": {Links: []string{"demo-db-1:database", "demo-db-1:db-1", "demo-db-1:demo-db-1", "outside:ext"}},
		}},
	}
	res.Project.Services[0] = app

	// Act
	cfg := newAssembly(res).serviceConfig(app)

	// Assert
	assert.Equal(t, []string{"db:database"}, cfg.Links)
	assert.Equal(t, []string{"outside:ext"}, cfg.ExternalLinks)
	assert.Equal(t, types.DependsOnConfig{
		"db": {Condition: types.ServiceConditionStarted, Restart: true, Required: true},
	}, cfg.DependsOn)
}
