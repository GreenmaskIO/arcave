package composespec

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestAssembler_Assemble(t *testing.T) {
	// Arrange
	res := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{
			{
				Name:   "db",
				Image:  "postgres:16",
				Labels: map[string]string{api.ServiceLabel: "db", "tier": "data"},
				Containers: []model.Container{{
					Name: "demo-db-1",
					Labels: map[string]string{
						api.WorkingDirLabel:  "/srv/demo",
						api.ConfigFilesLabel: "/srv/demo/compose.yaml,/srv/demo/compose.override.yaml",
					},
				}},
			},
			{
				Name:       "web",
				Image:      "nginx:1",
				Containers: []model.Container{{Name: "demo-web-1"}, {Name: "demo-web-2"}},
			},
		},
		Volumes: []model.NamedVolume{
			{
				Key: "pgdata", Name: "demo_pgdata", Driver: "local",
				DriverOpts: map[string]string{"type": "none"},
				Labels:     map[string]string{api.VolumeLabel: "pgdata", "keep": "yes"},
			},
			{Key: "shared", Name: "shared", External: true, Driver: "local"},
		},
		Networks: []model.Network{{
			Key:  "back",
			Name: "demo_back",
			Inspect: network.Inspect{
				Driver:   "bridge",
				Internal: true,
				Options:  map[string]string{"com.docker.network.bridge.name": "br-demo"},
				IPAM: network.IPAM{
					Driver: "default",
					Config: []network.IPAMConfig{{Subnet: "172.28.0.0/16", Gateway: "172.28.0.1"}},
				},
				Labels: map[string]string{api.NetworkLabel: "back"},
			},
		}},
	}}

	// Act
	project, err := NewAssembler().Assemble(res)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "demo", project.Name)
	assert.Equal(t, "/srv/demo", project.WorkingDir)
	assert.Equal(t, []string{"/srv/demo/compose.yaml", "/srv/demo/compose.override.yaml"}, project.ComposeFiles)

	require.Len(t, project.Services, 2)
	assert.Equal(t, "postgres:16", project.Services["db"].Image)
	assert.Equal(t, pullPolicyMissing, project.Services["db"].PullPolicy)
	// Compose re-stamps its own labels on create; only the user's survive.
	assert.Equal(t, types.Labels{"tier": "data"}, project.Services["db"].Labels)
	// Scale is the replica count: compose skips a service scaled to zero.
	require.NotNil(t, project.Services["db"].Scale)
	assert.Equal(t, 1, *project.Services["db"].Scale)
	require.NotNil(t, project.Services["web"].Scale)
	assert.Equal(t, 2, *project.Services["web"].Scale)

	assert.Equal(t, types.VolumeConfig{
		Name:       "demo_pgdata",
		Driver:     "local",
		DriverOpts: types.Options{"type": "none"},
		Labels:     types.Labels{"keep": "yes"},
	}, project.Volumes["pgdata"])
	// An external volume takes no other key: compose rejects a declaration of what it does not own.
	assert.Equal(t, types.VolumeConfig{Name: "shared", External: true}, project.Volumes["shared"])

	back := project.Networks["back"]
	assert.Equal(t, "demo_back", back.Name)
	assert.Equal(t, "bridge", back.Driver)
	assert.True(t, back.Internal)
	assert.Equal(t, types.Options{"com.docker.network.bridge.name": "br-demo"}, back.DriverOpts)
	assert.Equal(t, "default", back.Ipam.Driver)
	require.Len(t, back.Ipam.Config, 1)
	assert.Equal(t, "172.28.0.0/16", back.Ipam.Config[0].Subnet)
	assert.Equal(t, "172.28.0.1", back.Ipam.Config[0].Gateway)
}

// A network the project attaches to but does not own carries no compose label, so
// only the containers name it.
func TestAssembler_Assemble_ForeignNetwork(t *testing.T) {
	// Arrange
	res := model.IntrospectionResult{Project: model.Project{
		Name: "demo",
		Services: []model.Service{{
			Name:  "app",
			Image: "app:1",
			Containers: []model.Container{{
				Name: "demo-app-1",
				Inspect: container.InspectResponse{
					ContainerJSONBase: &container.ContainerJSONBase{ID: "abc"},
					NetworkSettings: &container.NetworkSettings{
						Networks: map[string]*network.EndpointSettings{
							"demo_back": {Aliases: []string{"app", "front"}},
							"corporate": {Aliases: []string{"app"}},
							"host":      {},
						},
					},
				},
			}},
		}},
		Networks: []model.Network{{Key: "back", Name: "demo_back"}},
	}}

	// Act
	project, err := NewAssembler().Assemble(res)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, types.NetworkConfig{Name: "corporate", External: true}, project.Networks["corporate"])
	assert.NotContains(t, project.Networks, "host")

	attached := project.Services["app"].Networks
	require.Contains(t, attached, "back")
	require.Contains(t, attached, "corporate")
	assert.NotContains(t, attached, "host")
	// The service name is compose's own alias, not a declaration.
	assert.Equal(t, []string{"front"}, attached["back"].Aliases)
}

// Required stays false: a dependency whose containers are gone must not fail the graph.
func TestAssembler_Assemble_DependsOn(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  types.DependsOnConfig
	}{
		{name: "no label", label: "", want: nil},
		{
			name:  "a service alone falls back to the default condition",
			label: "db",
			want:  types.DependsOnConfig{"db": {Condition: dependencyCondition, Restart: true}},
		},
		{
			name:  "service and condition",
			label: "db:service_healthy",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: true}},
		},
		{
			name:  "service, condition and restart",
			label: "db:service_healthy:false",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: false}},
		},
		{
			name:  "several dependencies",
			label: "db:service_healthy:true,cache:service_started:true",
			want: types.DependsOnConfig{
				"db":    {Condition: "service_healthy", Restart: true},
				"cache": {Condition: "service_started", Restart: true},
			},
		},
		{
			name:  "a dependency the stack no longer holds is dropped",
			label: "db:service_healthy,gone:service_started",
			want:  types.DependsOnConfig{"db": {Condition: "service_healthy", Restart: true}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			res := model.IntrospectionResult{Project: model.Project{
				Name: "demo",
				Services: []model.Service{
					{
						Name:       "app",
						Labels:     map[string]string{api.DependenciesLabel: tt.label},
						Containers: []model.Container{{Name: "demo-app-1"}},
					},
					{Name: "db", Containers: []model.Container{{Name: "demo-db-1"}}},
					{Name: "cache", Containers: []model.Container{{Name: "demo-cache-1"}}},
				},
			}}

			// Act
			project, err := NewAssembler().Assemble(res)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, project.Services["app"].DependsOn)
			for name, dep := range project.Services["app"].DependsOn {
				assert.False(t, dep.Required, "dependency %q must not be required", name)
			}
		})
	}
}

func TestAssembler_Assemble_NoServices(t *testing.T) {
	// Act
	_, err := NewAssembler().Assemble(model.IntrospectionResult{Project: model.Project{Name: "demo"}})

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"demo"`)
}
