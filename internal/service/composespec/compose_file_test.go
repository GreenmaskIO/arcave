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
	"context"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/utils"
)

func TestWithRelativeBinds(t *testing.T) {
	tests := []struct {
		name   string
		dir    string
		source string
		want   string
	}{
		{name: "under the dir", dir: "/srv/demo", source: "/srv/demo/conf/app", want: "./conf/app"},
		{name: "the dir itself", dir: "/srv/demo", source: "/srv/demo", want: "."},
		{name: "outside the dir", dir: "/srv/demo", source: "/var/lib/data", want: "/var/lib/data"},
		{name: "a sibling sharing the prefix", dir: "/srv/demo", source: "/srv/demo-old/conf", want: "/srv/demo-old/conf"},
		{name: "the parent", dir: "/srv/demo", source: "/srv", want: "/srv"},
		{name: "no working dir", dir: "", source: "/srv/demo/conf", want: "/srv/demo/conf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			project := &types.Project{Name: "demo", WorkingDir: tt.dir, Services: types.Services{"app": {
				Name: "app",
				Volumes: []types.ServiceVolumeConfig{
					{Type: types.VolumeTypeBind, Source: tt.source, Target: "/conf"},
					{Type: types.VolumeTypeVolume, Source: "/srv/demo/named", Target: "/data"},
				},
			}}}

			// Act
			got := withRelativeBinds(project)

			// Assert
			assert.Equal(t, tt.want, got.Services["app"].Volumes[0].Source)
			assert.Equal(t, "/srv/demo/named", got.Services["app"].Volumes[1].Source, "only a bind is rewritten")
			assert.Equal(t, tt.source, project.Services["app"].Volumes[0].Source, "the input is not modified")
		})
	}
}

func TestComposeFileCodec_MarshalParse(t *testing.T) {
	// Arrange
	codec := NewComposeFileCodec()
	project, err := NewAssembler().Assemble(context.Background(), richResult())
	require.NoError(t, err)
	require.NotEmpty(t, project.WorkingDir)
	service := project.Services["db"]
	service.Environment = types.MappingWithEquals{"PASS": utils.New("a$b"), "REF": utils.New("${HOME}")}
	service.HealthCheck = &types.HealthCheckConfig{Test: types.HealthCheckTest{"CMD-SHELL", "pg_isready -U $POSTGRES_USER"}}
	project.Services["db"] = service

	// Act
	spec, err := codec.Marshal(project)

	// Assert
	require.NoError(t, err)
	assert.NotContains(t, string(spec), specOrigin)
	parsed, err := codec.Parse(context.Background(), spec)
	require.NoError(t, err, "%s", spec)
	assert.Equal(t, project.ServiceNames(), parsed.ServiceNames())
	assert.Empty(t, parsed.WorkingDir, "the file names no origin")
	got := parsed.Services["db"]
	assert.Equal(t, service.Environment, got.Environment, "compose reads every value back as the container has it")
	assert.Equal(t, service.HealthCheck.Test, got.HealthCheck.Test)
}

// The rendered file spells a bind under the working directory relative to it.
func TestComposeFileCodec_Marshal_RelativeBinds(t *testing.T) {
	// Arrange
	codec := NewComposeFileCodec()
	project := &types.Project{Name: "demo", WorkingDir: "/srv/demo", Services: types.Services{"app": {
		Name:    "app",
		Image:   "busybox",
		Volumes: []types.ServiceVolumeConfig{{Type: types.VolumeTypeBind, Source: "/srv/demo/conf", Target: "/conf"}},
	}}}

	// Act
	spec, err := codec.Marshal(project)

	// Assert
	require.NoError(t, err)
	parsed, err := codec.Parse(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "./conf", parsed.Services["app"].Volumes[0].Source)
	assert.Equal(t, "/srv/demo/conf", project.Services["app"].Volumes[0].Source, "the input is not modified")
}

func TestComposeFileCodec_Marshal_NoProject(t *testing.T) {
	// Act
	_, err := NewComposeFileCodec().Marshal(nil)

	// Assert
	require.ErrorIs(t, err, errNoProject)
}
