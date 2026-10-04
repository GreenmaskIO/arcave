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
	"testing"

	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
)

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
