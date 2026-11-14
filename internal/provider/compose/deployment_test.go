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
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

func TestNewDeployedReplicas(t *testing.T) {
	// Arrange
	scaleTwo, scaleZero := 2, 0
	project := &types.Project{Name: demoProject, Services: types.Services{
		"db":      {Name: "db"},
		"web":     {Name: "web", Scale: &scaleTwo},
		"app":     {Name: "app"},
		"migrate": {Name: "migrate"},
		"idle":    {Name: "idle", Scale: &scaleZero},
	}}
	recorded := []recordedReplica{
		{service: "db", number: 3},
		{service: "db", number: 1},
		{service: "app", number: 1},
	}

	// Act
	replicas := newDeployedReplicas(project, recorded)

	// Assert
	assert.Equal(t, map[string][]int{
		"db":      {1, 3},
		"web":     {1, 2},
		"app":     {1},
		"migrate": {1},
	}, replicas, "a recorded gap stays, a service recorded with no replica runs at its scale, one scaled to none is left out")
}

func TestWithDeploymentLabels(t *testing.T) {
	// Arrange
	project := &types.Project{
		Name: demoProject,
		Services: types.Services{
			demoService: {Name: demoService, Labels: types.Labels{"keep": "me"}},
		},
		Volumes:  types.Volumes{"db": {Name: demoVolumeName}},
		Networks: types.Networks{"default": {Name: "demo_default"}},
	}

	// Act
	labeled := withDeploymentLabels(project, map[string]string{string(model.SystemLabelDeploymentSavepointID): "id"})

	// Assert
	assert.Equal(t, types.Labels{"keep": "me", string(model.SystemLabelDeploymentSavepointID): "id"},
		labeled.Services[demoService].Labels)
	assert.Equal(t, types.Labels{"keep": "me"}, project.Services[demoService].Labels, "the project handed in is left as it is")
	assert.Nil(t, labeled.Volumes["db"].Labels, "a volume's labels are in compose's config hash")
	assert.Nil(t, labeled.Networks["default"].Labels, "and so are a network's")
}

// matchDeploymentOf matches target with its every service labelled with a deployment, and its
// volumes and networks with none: their labels are in compose's config hash.
func matchDeploymentOf(target *types.Project) func(*types.Project) bool {
	return func(project *types.Project) bool {
		for _, volume := range project.Volumes {
			if hasDeploymentLabel(volume.Labels) {
				return false
			}
		}
		for _, network := range project.Networks {
			if hasDeploymentLabel(network.Labels) {
				return false
			}
		}
		return len(project.Services) == len(target.Services) &&
			assert.ObjectsAreEqual(target, withoutDeploymentLabels(project))
	}
}

func hasDeploymentLabel(labels types.Labels) bool {
	for key := range labels {
		if strings.HasPrefix(key, model.DeploymentLabelPrefix) {
			return true
		}
	}
	return false
}
