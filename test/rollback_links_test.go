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

package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// linkedReplicasSpec is a replicated service another one reaches through a legacy link alias.
// On a compose network the link is a DNS alias on client's endpoint, naming each replica.
const linkedReplicasSpec = `
name: %[1]s
services:
  worker:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: %[2]d
    volumes: [/cache]
  client:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    links: ["worker:w"]
`

func TestRollback_KeepsALinkToARebuiltReplica(t *testing.T) {
	tests := []struct {
		name     string
		replicas int
		remove   int // the replica removed before the savepoint; 0 removes none
		// knownDefect names the BACKLOG.md row this case holds: it asserts the defect, and
		// turns red once the defect is fixed.
		knownDefect string
	}{
		{name: "a replica rebuilt under its own name", replicas: 2},
		{
			name: "a replica renumbered across a gap", replicas: 3, remove: 2,
			knownDefect: "a renumbered replica breaks a link to its old name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			env := newImageRollbackEnv(t)
			env.upDocument(t, fmt.Sprintf(linkedReplicasSpec, env.project, tt.replicas))
			require.Empty(t, env.danglingLinks(t, "client-1"))
			if tt.remove != 0 {
				require.NoError(t, env.daemon.ContainerRemove(env.ctx,
					env.containers(t)[fmt.Sprintf("worker-%d", tt.remove)].ID,
					container.RemoveOptions{Force: true, RemoveVolumes: true}))
			}
			savepoint, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "linked"})
			require.NoError(t, err)

			// Act
			_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &savepoint.ID})

			// Assert
			require.NoError(t, err)
			if tt.knownDefect != "" {
				assert.NotEmpty(t, env.danglingLinks(t, "client-1"), "known defect %q is fixed: flip this case", tt.knownDefect)
				return
			}
			assert.Empty(t, env.danglingLinks(t, "client-1"), "every link names a container that exists")
		})
	}
}

// danglingLinks are the links of a container that name no container of the compose project.
// Docker's DNS answers an alias through them, and fails on one that names nothing.
func (e *imageRollbackEnv) danglingLinks(t *testing.T, name string) []string {
	t.Helper()

	containers := e.containers(t)
	existing := map[string]bool{}
	for _, c := range containers {
		existing[strings.TrimPrefix(c.Inspect.Name, "/")] = true
	}
	var dangling []string
	for _, endpoint := range containers[name].Inspect.NetworkSettings.Networks {
		for _, link := range endpoint.Links {
			target, _, _ := strings.Cut(link, ":")
			if !existing[target] {
				dangling = append(dangling, link)
			}
		}
	}
	return dangling
}
