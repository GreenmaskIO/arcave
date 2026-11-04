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
	"context"
	"strings"
	"testing"

	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
)

// removeComposeProject drops everything a test left of project, images included. It goes
// straight to the daemon: compose.Client.Down keeps volumes on purpose.
func removeComposeProject(t *testing.T, cli *client.Client, project string) {
	t.Helper()

	ctx := context.Background()
	byProject := filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+project))

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: byProject})
	if assert.NoError(t, err, "list containers of %s", project) {
		removeContainers(t, cli, containers)
	}
	volumes, err := cli.VolumeList(ctx, volume.ListOptions{Filters: byProject})
	if assert.NoError(t, err, "list volumes of %s", project) {
		for _, v := range volumes.Volumes {
			assert.NoError(t, cli.VolumeRemove(ctx, v.Name, true), "remove volume %s", v.Name)
		}
	}
	removeProjectNetworks(t, cli, project)
	images, err := cli.ImageList(ctx, image.ListOptions{All: true})
	if assert.NoError(t, err, "list images") {
		for _, img := range images {
			for _, repoTag := range img.RepoTags {
				// A rollback's layer tags, and the images compose built for the project, as
				// Docker spells them: Podman prefixes docker.io/.
				tag := string(model.ImageRef(repoTag).Familiar())
				if strings.HasPrefix(tag, "arcave/"+project+"-") || strings.HasPrefix(tag, project+"-") {
					_, err := cli.ImageRemove(ctx, tag, image.RemoveOptions{PruneChildren: true})
					assert.NoError(t, err, "remove image %s", tag)
				}
			}
		}
	}
}

// removeContainers removes every container, the replica's anonymous volumes with it, in
// passes: Podman refuses to remove one whose ipc or pid namespace another still joins, and a
// summary does not say which. It asserts what a pass that removes nothing leaves.
func removeContainers(t *testing.T, cli *client.Client, containers []container.Summary) {
	t.Helper()

	pending := containers
	for len(pending) > 0 {
		var held []container.Summary
		errs := map[string]error{}
		for _, c := range pending {
			if err := cli.ContainerRemove(context.Background(), c.ID,
				container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				held = append(held, c)
				errs[c.ID] = err
			}
		}
		if len(held) == len(pending) {
			for _, c := range held {
				assert.NoError(t, errs[c.ID], "remove container %s", c.ID)
			}
			return
		}
		pending = held
	}
}

// removeProjectNetworks drops the networks compose created for project. A Down called by
// name alone removes the containers but not these, and every leaked one holds an address
// pool until the daemon has none left.
func removeProjectNetworks(t *testing.T, cli *client.Client, project string) {
	t.Helper()

	ctx := context.Background()
	networks, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+project)),
	})
	if !assert.NoError(t, err, "list networks of %s", project) {
		return
	}
	for _, n := range networks {
		assert.NoError(t, cli.NetworkRemove(ctx, n.ID), "remove network %s", n.Name)
	}
}
