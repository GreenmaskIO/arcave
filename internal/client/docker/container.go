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
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// RecreateWithImage rebuilds one created container from another image, keeping
// its name, labels, host config and declared endpoints, and returns the new id.
// The container must be stopped and its data not yet restored: the anonymous
// volumes compose just made for it go with it.
func (e *Engine) RecreateWithImage(ctx context.Context, containerID, imageRef string) (string, error) {
	inspect, err := e.api.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspect container %s: %w", containerID, err)
	}
	if inspect.Config == nil || inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return "", fmt.Errorf("container %s reports no configuration to rebuild it from", containerID)
	}

	// Config and HostConfig go over whole — environment, labels, healthcheck, mounts,
	// capabilities, resources and the rest are the daemon's own account of a container
	// compose has just created, not a reconstruction of it.
	config := *inspect.Config
	config.Image = imageRef
	config.Hostname = declaredHostname(inspect)
	hostConfig := *inspect.HostConfig
	name := strings.TrimPrefix(inspect.Name, "/")
	networking := declaredEndpoints(inspect.NetworkSettings)
	platform := imagePlatform(inspect)

	// The empty anonymous volumes compose made for this container go with it; the
	// replacement is handed fresh ones.
	if err := e.api.ContainerRemove(ctx, containerID, container.RemoveOptions{RemoveVolumes: true}); err != nil {
		return "", fmt.Errorf("remove container %s: %w", containerID, err)
	}

	created, err := e.api.ContainerCreate(ctx, &config, &hostConfig, networking, platform, name)
	if err != nil {
		return "", fmt.Errorf("recreate container %s from %s: %w", name, imageRef, err)
	}
	return created.ID, nil
}

// declaredHostname drops a hostname the daemon derived from the container being
// replaced — it defaults an empty one to the short id — so the replacement is
// given its own rather than inheriting a name for a container that is gone.
func declaredHostname(inspect container.InspectResponse) string {
	const shortIDLen = 12
	if len(inspect.ID) >= shortIDLen && inspect.Config.Hostname == inspect.ID[:shortIDLen] {
		return ""
	}
	return inspect.Config.Hostname
}

// imagePlatform carries the platform of the image the container was created from,
// so an emulated replica rebuilds emulated. Nil when the daemon reports none, which
// is what compose itself passes for a service declaring no platform.
func imagePlatform(inspect container.InspectResponse) *ocispec.Platform {
	if inspect.ImageManifestDescriptor == nil {
		return nil
	}
	return inspect.ImageManifestDescriptor.Platform
}

// declaredEndpoints keeps what was declared and drops what the daemon assigned:
// an address or endpoint id belongs to the container being replaced.
func declaredEndpoints(settings *container.NetworkSettings) *network.NetworkingConfig {
	endpoints := map[string]*network.EndpointSettings{}
	if settings != nil {
		for name, endpoint := range settings.Networks {
			if endpoint == nil {
				continue
			}
			declared := &network.EndpointSettings{
				IPAMConfig: endpoint.IPAMConfig,
				Links:      endpoint.Links,
				Aliases:    endpoint.Aliases,
				MacAddress: endpoint.MacAddress,
				DriverOpts: endpoint.DriverOpts,
				GwPriority: endpoint.GwPriority,
			}
			if endpoint.IPAMConfig != nil {
				declared.IPAMConfig = endpoint.IPAMConfig.Copy()
			}
			endpoints[name] = declared
		}
	}
	return &network.NetworkingConfig{EndpointsConfig: endpoints}
}
