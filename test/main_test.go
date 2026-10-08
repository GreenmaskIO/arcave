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
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// testResource matches what a test names after its compose project, arcave-<kind>-<suffix>,
// and the layer tags a rollback of one leaves: arcave/arcave-<kind>-<suffix>-….
var testResource = regexp.MustCompile(`^(arcave/)?arcave-[a-z-]+-([0-9a-f]{10})([-_]|$)`)

// issuedSuffixes is every suffix randomSuffix handed out in this process: a resource named
// with another one belongs to a concurrent run on the same daemon, not to this one.
var issuedSuffixes sync.Map

// anonymousVolumeLabel is what the daemon stamps on a volume it created for a container.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// resource is one thing the daemon holds: a container, volume, anonymous volume, network or image.
type resource struct{ kind, name string }

func (r resource) String() string { return r.kind + " " + r.name }

// TestMain fails the run when a test leaves anything of its own in the daemon. A leftover
// anonymous volume is only reported: the daemon is shared, and it carries no test's name.
func TestMain(m *testing.M) {
	flag.Parse()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil || testing.Short() {
		os.Exit(m.Run())
	}
	defer func() { _ = cli.Close() }()
	before, err := daemonResources(cli)
	if err != nil {
		os.Exit(m.Run())
	}

	code := m.Run()

	after, err := daemonResources(cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leak check: %v\n", err)
		os.Exit(code)
	}
	var leaked, anonymous []string
	for r := range after {
		if before[r] {
			continue
		}
		switch {
		case ownResource(r.name):
			leaked = append(leaked, r.String())
		case r.kind == "anonymous volume":
			anonymous = append(anonymous, r.String())
		}
	}
	if len(anonymous) > 0 {
		slices.Sort(anonymous)
		fmt.Fprintf(os.Stderr, "leak check: %d anonymous volume(s) appeared during the run:\n  %s\n",
			len(anonymous), strings.Join(anonymous, "\n  "))
	}
	if len(leaked) > 0 {
		slices.Sort(leaked)
		fmt.Fprintf(os.Stderr, "leak check: the run left %d resource(s) behind:\n  %s\n",
			len(leaked), strings.Join(leaked, "\n  "))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// ownResource reports whether name is a test resource of this process.
func ownResource(name string) bool {
	m := testResource.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	_, ok := issuedSuffixes.Load(m[2])
	return ok
}

// daemonResources is every container, volume, network and image tag the daemon holds.
func daemonResources(cli *client.Client) (map[resource]bool, error) {
	ctx := context.Background()
	if _, err := cli.Ping(ctx); err != nil {
		return nil, err
	}
	held := map[resource]bool{}

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		for _, name := range c.Names {
			held[resource{"container", strings.TrimPrefix(name, "/")}] = true
		}
	}
	volumes, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range volumes.Volumes {
		if _, ok := v.Labels[anonymousVolumeLabel]; ok {
			held[resource{"anonymous volume", v.Name}] = true
			continue
		}
		held[resource{"volume", v.Name}] = true
	}
	networks, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range networks {
		held[resource{"network", n.Name}] = true
	}
	images, err := cli.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			held[resource{"image", tag}] = true
		}
	}
	return held, nil
}
