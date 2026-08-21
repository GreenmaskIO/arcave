package composespec

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v2/pkg/api"
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
				Name:       "db",
				Image:      "postgres:16",
				Labels:     map[string]string{api.ServiceLabel: "db"},
				Containers: []model.Container{{Name: "demo-db-1"}},
			},
			{
				Name:       "web",
				Image:      "nginx:1",
				Containers: []model.Container{{Name: "demo-web-1"}, {Name: "demo-web-2"}},
			},
		},
		Volumes: []model.NamedVolume{{
			Key: "pgdata", Name: "demo_pgdata", Driver: "local",
			Labels: map[string]string{api.VolumeLabel: "pgdata"},
		}},
		Networks: []model.Network{{
			Key:  "back",
			Name: "demo_back",
			Inspect: network.Inspect{
				Driver: "bridge",
				Labels: map[string]string{api.NetworkLabel: "back"},
			},
		}},
	}}

	// Act
	project, err := NewAssembler().Assemble(res)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "demo", project.Name)

	require.Len(t, project.Services, 2)
	assert.Equal(t, "postgres:16", project.Services["db"].Image)
	assert.Equal(t, types.Labels{api.ServiceLabel: "db"}, project.Services["db"].Labels)
	// Scale is the replica count: compose skips a service scaled to zero.
	require.NotNil(t, project.Services["db"].Scale)
	assert.Equal(t, 1, *project.Services["db"].Scale)
	require.NotNil(t, project.Services["web"].Scale)
	assert.Equal(t, 2, *project.Services["web"].Scale)

	assert.Equal(t, types.Volumes{"pgdata": {
		Name: "demo_pgdata", Driver: "local", Labels: types.Labels{api.VolumeLabel: "pgdata"},
	}}, project.Volumes)
	assert.Equal(t, types.Networks{"back": {
		Name: "demo_back", Driver: "bridge", Labels: types.Labels{api.NetworkLabel: "back"},
	}}, project.Networks)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			res := model.IntrospectionResult{Project: model.Project{
				Name: "demo",
				Services: []model.Service{{
					Name:       "app",
					Labels:     map[string]string{api.DependenciesLabel: tt.label},
					Containers: []model.Container{{Name: "demo-app-1"}},
				}},
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
