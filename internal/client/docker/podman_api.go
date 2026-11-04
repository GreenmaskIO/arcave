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

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-units"
)

// mountAPIBindOption is the option libpod reports a bind of the mount API with; the bind
// API's carries rbind.
const mountAPIBindOption = "bind"

// logMaxSizeOption is the log option Docker keeps the size libpod reports as LogConfig.Size in.
const logMaxSizeOption = "max-size"

// PodmanAPI is the Docker API of a Podman daemon, answering a container inspect as Docker
// would: with the subpath mounts and log size compat drops and libpod keeps.
type PodmanAPI struct {
	client.APIClient
	libpod Libpod
}

// NewPodmanAPI wraps api, connected to a Podman daemon, with libpod over the same connection.
func NewPodmanAPI(api client.APIClient, libpod Libpod) *PodmanAPI {
	return &PodmanAPI{
		APIClient: api,
		libpod:    libpod,
	}
}

// libpodContainer is the part of libpod's container inspect compat leaves out.
type libpodContainer struct {
	Mounts     []libpodMount `json:"Mounts"`
	HostConfig struct {
		LogConfig struct {
			Size string `json:"Size"`
		} `json:"LogConfig"`
	} `json:"HostConfig"`
}

// libpodMount is one mount as libpod reports it.
type libpodMount struct {
	Type        string   `json:"Type"`
	Name        string   `json:"Name"`
	Source      string   `json:"Source"`
	Destination string   `json:"Destination"`
	Options     []string `json:"Options"`
	RW          bool     `json:"RW"`
	Propagation string   `json:"Propagation"`
	SubPath     string   `json:"SubPath"`
}

// ContainerInspect is the compat inspect with libpod's subpath and mount-API bind mounts
// as mount requests, and its log size as the max-size option.
func (p *PodmanAPI) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	inspect, err := p.APIClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return container.InspectResponse{}, err
	}
	stored, err := readLibpodContainer(ctx, p.libpod, containerID)
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container %s through libpod: %w", containerID, err)
	}
	applyLibpodRequest(&inspect, stored)
	return inspect, nil
}

// readLibpodContainer reads libpod's own inspect of one container.
func readLibpodContainer(ctx context.Context, libpod Libpod, containerID string) (libpodContainer, error) {
	resp, err := callLibpod(ctx, libpod, http.MethodGet, nil, "containers", containerID, "json")
	if err != nil {
		return libpodContainer{}, err
	}
	//nolint:errcheck // the body is read whole below
	defer resp.Body.Close()
	var stored libpodContainer
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		return libpodContainer{}, fmt.Errorf("decode libpod's answer: %w", err)
	}
	return stored, nil
}

// applyLibpodRequest adds to inspect the subpath mounts and log size libpod reports.
func applyLibpodRequest(inspect *container.InspectResponse, stored libpodContainer) {
	if inspect.ContainerJSONBase == nil {
		return
	}
	hostConfig := inspect.HostConfig
	hostConfig.Mounts = append(hostConfig.Mounts, newMountRequests(stored.Mounts)...)
	applyLogSize(&hostConfig.LogConfig, stored.HostConfig.LogConfig.Size)
}

// newMountRequests are the mount requests compat drops: a volume mounted at a subpath, and
// a bind of the mount API, marked bind where the bind API's carries rbind.
func newMountRequests(mounts []libpodMount) []mount.Mount {
	var requests []mount.Mount
	for _, stored := range mounts {
		switch {
		case stored.Type == string(mount.TypeVolume) && stored.SubPath != "":
			requests = append(requests, mount.Mount{
				Type:          mount.TypeVolume,
				Source:        stored.Name,
				Target:        stored.Destination,
				ReadOnly:      !stored.RW,
				VolumeOptions: &mount.VolumeOptions{Subpath: stored.SubPath},
			})
		case stored.Type == string(mount.TypeBind) && slices.Contains(stored.Options, mountAPIBindOption):
			requests = append(requests, mount.Mount{
				Type:        mount.TypeBind,
				Source:      stored.Source,
				Target:      stored.Destination,
				ReadOnly:    !stored.RW,
				BindOptions: &mount.BindOptions{Propagation: mount.Propagation(stored.Propagation)},
			})
		}
	}
	return requests
}

// applyLogSize sets the size libpod reports as logConfig's max-size, which compat
// drops; libpod reports none as a negative size.
func applyLogSize(logConfig *container.LogConfig, size string) {
	if bytes, err := units.RAMInBytes(size); err != nil || bytes <= 0 {
		return
	}
	if logConfig.Config == nil {
		logConfig.Config = map[string]string{}
	}
	logConfig.Config[logMaxSizeOption] = size
}
