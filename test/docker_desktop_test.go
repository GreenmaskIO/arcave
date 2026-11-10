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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/client/dockerdesktop"
	"github.com/greenmaskio/arcave/internal/model"
)

// desktopBindTarget is where every container of these tests mounts the bind under test.
const desktopBindTarget = "/host"

// Docker Desktop spells a bind back on the host only while no mount of another type comes before
// it; the profile's API spells every one there, whatever the order compose happened to send.
func TestDockerDesktop_ContainerInspect_BindOnTheHost(t *testing.T) {
	t.Parallel()

	daemon := newDesktopDaemon(t)
	profile := newRuntimeProfile(t, daemon)
	suffix := randomSuffix(t)
	volume := "arcave-desktop-it-" + suffix
	createVolume(t, daemon, volume)
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	tests := []struct {
		name       string
		hostConfig container.HostConfig
		want       string
	}{
		{
			name: "the mount API, a volume first",
			hostConfig: container.HostConfig{Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: volume, Target: "/v"},
				{Type: mount.TypeBind, Source: dir, Target: desktopBindTarget},
			}},
			want: dir,
		},
		{
			name:       "the bind API, a volume first",
			hostConfig: container.HostConfig{Binds: []string{volume + ":/v", dir + ":" + desktopBindTarget}},
			want:       dir,
		},
		{
			name: "a tmpfs first",
			hostConfig: container.HostConfig{Mounts: []mount.Mount{
				{Type: mount.TypeTmpfs, Target: "/t"},
				{Type: mount.TypeBind, Source: dir, Target: desktopBindTarget},
			}},
			want: dir,
		},
		{
			name: "a symlink, as it was asked for",
			hostConfig: container.HostConfig{Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: volume, Target: "/v"},
				{Type: mount.TypeBind, Source: link, Target: desktopBindTarget},
			}},
			want: link,
		},
		{
			name: "created on the VM's path, which Docker Desktop records nothing for",
			hostConfig: container.HostConfig{Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: volume, Target: "/v"},
				{Type: mount.TypeBind, Source: "/host_mnt" + resolved, Target: desktopBindTarget},
			}},
			want: resolved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			id := createDesktopContainer(t, daemon, tt.hostConfig)

			// Act
			got, err := profile.api.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, bindSources(got.Container))
		})
	}
}

// A replica rebuilt from its inspect is created on the host path, so Docker Desktop records it again.
func TestDockerDesktop_RecreateWithImage_KeepsTheHostPath(t *testing.T) {
	t.Parallel()

	// Arrange
	daemon := newDesktopDaemon(t)
	profile := newRuntimeProfile(t, daemon)
	suffix := randomSuffix(t)
	volume := "arcave-desktop-it-" + suffix
	createVolume(t, daemon, volume)
	dir := t.TempDir()
	id := createDesktopContainer(t, daemon, container.HostConfig{Mounts: []mount.Mount{
		{Type: mount.TypeVolume, Source: volume, Target: "/v"},
		{Type: mount.TypeBind, Source: dir, Target: desktopBindTarget},
	}})
	name := "arcave-desktop-it-rebuilt-" + suffix

	// Act
	rebuilt, err := profile.engine.RecreateWithImage(context.Background(), id,
		model.ReplicaIdentity{Name: name, Number: 1}, busyboxImage)

	// Assert
	require.NoError(t, err)
	t.Cleanup(func() { removeDesktopContainer(daemon, rebuilt) })
	listed, err := daemon.ContainerList(context.Background(), client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("id", rebuilt),
	})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	assert.Equal(t, dir, listed.Items[0].Labels["desktop.docker.io/mounts/1/Source"], "the host path, recorded again")
}

// newDesktopDaemon skips the test unless a Docker Desktop daemon answers.
func newDesktopDaemon(t *testing.T) *client.Client {
	t.Helper()

	daemon := newDaemonClient(t)
	answer, err := daemon.Info(context.Background(), client.InfoOptions{})
	require.NoError(t, err)
	if answer.Info.OperatingSystem != dockerdesktop.OperatingSystem {
		t.Skipf("the daemon is %q, not Docker Desktop", answer.Info.OperatingSystem)
	}
	return daemon
}

// createDesktopContainer creates a busybox container mounting as hostConfig asks, removed with the test.
func createDesktopContainer(t *testing.T, daemon *client.Client, hostConfig container.HostConfig) string {
	t.Helper()

	created, err := daemon.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Config:     &container.Config{Image: busyboxImage, Cmd: []string{"true"}},
		HostConfig: &hostConfig,
	})
	require.NoError(t, err)
	t.Cleanup(func() { removeDesktopContainer(daemon, created.ID) })
	return created.ID
}

func removeDesktopContainer(daemon *client.Client, id string) {
	_, _ = daemon.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
}

// bindSources are the host paths an inspect names for desktopBindTarget, in each place it names one.
func bindSources(inspect container.InspectResponse) []string {
	sources := map[string]bool{}
	for _, mountPoint := range inspect.Mounts {
		if mountPoint.Destination == desktopBindTarget {
			sources[mountPoint.Source] = true
		}
	}
	for _, request := range inspect.HostConfig.Mounts {
		if request.Target == desktopBindTarget {
			sources[request.Source] = true
		}
	}
	for _, entry := range inspect.HostConfig.Binds {
		if source, rest, _ := strings.Cut(entry, ":"); strings.HasPrefix(rest, desktopBindTarget) {
			sources[source] = true
		}
	}
	return slices.Sorted(maps.Keys(sources))
}
