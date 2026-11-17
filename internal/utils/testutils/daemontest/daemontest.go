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

// Package daemontest is what every suite run against a live daemon shares: the client, the
// suffix that names a test's resources, their removal, and a leak check over the whole run.
package daemontest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// testResource matches what a test names after its compose project, arcave-<kind>-<suffix>,
// and the layer tags a rollback of one leaves: arcave/arcave-<kind>-<suffix>-….
var testResource = regexp.MustCompile(`^(arcave/)?arcave-[a-z-]+-([0-9a-f]{10})([-_]|$)`)

// issuedSuffixes is every suffix RandomSuffix handed out in this process: a resource named
// with another one belongs to a concurrent run on the same daemon, not to this one.
var issuedSuffixes sync.Map

// anonymousVolumeLabel is what the daemon stamps on a volume it created for a container.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// ryukDisabled is the variable testcontainers reads before it starts its reaper.
const ryukDisabled = "TESTCONTAINERS_RYUK_DISABLED"

// resource is one thing the daemon holds: a container, volume, anonymous volume, network or image.
type resource struct{ kind, name string }

func (r resource) String() string { return r.kind + " " + r.name }

// Run runs the suite and fails it when a test leaves anything of its own in the daemon; a
// leftover anonymous volume is only reported: the daemon is shared, and it carries no test's name.
// It returns the exit code for TestMain's os.Exit.
func Run(m *testing.M) int {
	flag.Parse()
	daemon, err := client.New(client.FromEnv)
	if err != nil || testing.Short() {
		return m.Run()
	}
	defer func() { _ = daemon.Close() }()
	disableReaperOnPodman(daemon)
	before, err := readDaemonResources(daemon)
	if err != nil {
		return m.Run()
	}

	code := m.Run()

	after, err := readDaemonResources(daemon)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leak check: %v\n", err)
		return code
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
	return code
}

// NewClient skips the test in short mode or unless a docker daemon answers.
func NewClient(t *testing.T) *client.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping the integration test in short mode")
	}
	daemon, err := client.New(client.FromEnv)
	require.NoError(t, err)
	if _, err := daemon.Ping(context.Background(), client.PingOptions{}); err != nil {
		_ = daemon.Close()
		t.Skip("skipping the integration test: no reachable docker daemon: " + err.Error())
	}
	t.Cleanup(func() { _ = daemon.Close() })
	return daemon
}

// RandomSuffix keeps concurrent copies of a test apart on one daemon; Run's leak check knows
// every suffix it handed out.
func RandomSuffix(t *testing.T) string {
	t.Helper()

	b := make([]byte, 5)
	_, err := rand.Read(b)
	require.NoError(t, err)
	suffix := hex.EncodeToString(b)
	issuedSuffixes.Store(suffix, true)
	return suffix
}

// IsPodman reports whether daemon's daemon is Podman.
func IsPodman(ctx context.Context, daemon client.APIClient) (bool, error) {
	version, err := daemon.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return false, fmt.Errorf("read the daemon version: %w", err)
	}
	return model.NewContainerRuntime(version.Version, version.Components).Kind == model.ContainerRuntimePodman, nil
}

// SkipWithoutVolumeSubpath skips on a Docker older than the API that mounts a volume at a subpath.
func SkipWithoutVolumeSubpath(t *testing.T, daemon *client.Client) {
	t.Helper()

	const volumeSubpathAPI = "1.45"
	podman, err := IsPodman(context.Background(), daemon)
	require.NoError(t, err)
	if podman {
		return
	}
	version, err := daemon.ServerVersion(context.Background(), client.ServerVersionOptions{})
	require.NoError(t, err)
	if versions.LessThan(version.APIVersion, volumeSubpathAPI) {
		t.Skipf("the daemon's API %s mounts no volume at a subpath", version.APIVersion)
	}
}

// RemoveComposeProject drops everything a test left of project, images included. It goes
// straight to the daemon: a compose down keeps volumes.
func RemoveComposeProject(t *testing.T, daemon *client.Client, project string) {
	t.Helper()

	ctx := context.Background()
	byProject := make(client.Filters).Add("label", api.ProjectLabel+"="+project)

	containers, err := daemon.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: byProject})
	if assert.NoError(t, err, "list containers of %s", project) {
		removeContainers(t, daemon, containers.Items)
	}
	volumes, err := daemon.VolumeList(ctx, client.VolumeListOptions{Filters: byProject})
	if assert.NoError(t, err, "list volumes of %s", project) {
		for _, v := range volumes.Items {
			_, err := daemon.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true})
			assert.NoError(t, err, "remove volume %s", v.Name)
		}
	}
	RemoveProjectNetworks(t, daemon, project)
	images, err := daemon.ImageList(ctx, client.ImageListOptions{All: true})
	if assert.NoError(t, err, "list images") {
		for _, img := range images.Items {
			for _, repoTag := range img.RepoTags {
				// A rollback's layer tags, and the images compose built for the project, as
				// Docker spells them: Podman prefixes docker.io/.
				tag := string(model.ImageRef(repoTag).Familiar())
				if strings.HasPrefix(tag, "arcave/"+project+"-") || strings.HasPrefix(tag, project+"-") {
					_, err := daemon.ImageRemove(ctx, tag, client.ImageRemoveOptions{PruneChildren: true})
					assert.NoError(t, err, "remove image %s", tag)
				}
			}
		}
	}
}

// RemoveProjectNetworks drops the networks compose created for project. A down called by
// name alone removes the containers but not these, and every leaked one holds an address
// pool until the daemon has none left.
func RemoveProjectNetworks(t *testing.T, daemon *client.Client, project string) {
	t.Helper()

	ctx := context.Background()
	networks, err := daemon.NetworkList(ctx, client.NetworkListOptions{
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+project),
	})
	if !assert.NoError(t, err, "list networks of %s", project) {
		return
	}
	for _, n := range networks.Items {
		_, err := daemon.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
		assert.NoError(t, err, "remove network %s", n.Name)
	}
}

// disableReaperOnPodman turns testcontainers' reaper off on Podman, whose machine socket the
// reaper cannot mount; every test removes its own containers in t.Cleanup.
func disableReaperOnPodman(daemon *client.Client) {
	if _, set := os.LookupEnv(ryukDisabled); set {
		return
	}
	podman, err := IsPodman(context.Background(), daemon)
	if err != nil || !podman {
		return
	}
	_ = os.Setenv(ryukDisabled, "true")
}

// readDaemonResources is every container, volume, network and image tag the daemon holds.
func readDaemonResources(daemon *client.Client) (map[resource]bool, error) {
	ctx := context.Background()
	if _, err := daemon.Ping(ctx, client.PingOptions{}); err != nil {
		return nil, err
	}
	held := map[resource]bool{}

	containers, err := daemon.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, c := range containers.Items {
		for _, name := range c.Names {
			held[resource{"container", strings.TrimPrefix(name, "/")}] = true
		}
	}
	volumes, err := daemon.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range volumes.Items {
		if _, ok := v.Labels[anonymousVolumeLabel]; ok {
			held[resource{"anonymous volume", v.Name}] = true
			continue
		}
		held[resource{"volume", v.Name}] = true
	}
	networks, err := daemon.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range networks.Items {
		held[resource{"network", n.Name}] = true
	}
	images, err := daemon.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, img := range images.Items {
		for _, tag := range img.RepoTags {
			held[resource{"image", tag}] = true
		}
	}
	return held, nil
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

// removeContainers removes every container, the replica's anonymous volumes with it, in
// passes: Podman refuses to remove one whose ipc or pid namespace another still joins, and a
// summary does not say which. It asserts what a pass that removes nothing leaves.
func removeContainers(t *testing.T, daemon *client.Client, containers []container.Summary) {
	t.Helper()

	pending := containers
	for len(pending) > 0 {
		var held []container.Summary
		errs := map[string]error{}
		for _, c := range pending {
			if _, err := daemon.ContainerRemove(context.Background(), c.ID,
				client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
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
