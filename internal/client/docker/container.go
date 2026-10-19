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
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/greenmaskio/arcave/internal/model"
)

// RecreateWithImage rebuilds a stopped container from imageRef under the given name and replica
// number, keeping its labels, host config and endpoints; its anonymous volumes are dropped.
func (e *Engine) RecreateWithImage(
	ctx context.Context, containerID string, identity model.ReplicaIdentity, imageRef string,
) (string, error) {
	if identity.Name == "" || identity.Number < 1 {
		return "", fmt.Errorf("container %s: no replica identity to rebuild it as", containerID)
	}
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
	config.Labels = renumberedLabels(inspect.Config.Labels, identity.Number)
	hostConfig := *inspect.HostConfig
	name := strings.TrimPrefix(inspect.Name, "/")
	networking := newNetworkingConfig(inspect.NetworkSettings, name, identity.Name)
	platform := imagePlatform(inspect)

	// The empty anonymous volumes compose made for this container go with it; the
	// replacement is handed fresh ones.
	if err := e.api.ContainerRemove(ctx, containerID, container.RemoveOptions{RemoveVolumes: true}); err != nil {
		return "", fmt.Errorf("remove container %s: %w", containerID, err)
	}

	created, err := e.api.ContainerCreate(ctx, &config, &hostConfig, networking, platform, identity.Name)
	if err != nil {
		return "", fmt.Errorf("recreate container %s as %s from %s: %w", name, identity.Name, imageRef, err)
	}
	return created.ID, nil
}

// renumberedLabels is labels, copied, with the container number set to number.
func renumberedLabels(labels map[string]string, number int) map[string]string {
	renumbered := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		renumbered[k] = v
	}
	renumbered[composeNumberLabel] = strconv.Itoa(number)
	return renumbered
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

// newNetworkingConfig keeps what was declared, drops the addresses and ids the daemon
// assigned, and renames the alias compose gave each endpoint from oldName to newName.
func newNetworkingConfig(settings *container.NetworkSettings, oldName, newName string) *network.NetworkingConfig {
	endpoints := map[string]*network.EndpointSettings{}
	if settings != nil {
		for name, endpoint := range settings.Networks {
			if endpoint == nil {
				continue
			}
			declared := &network.EndpointSettings{
				IPAMConfig: endpoint.IPAMConfig,
				Links:      endpoint.Links,
				Aliases:    renamedAliases(endpoint.Aliases, oldName, newName),
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

// renamedAliases is aliases, copied, with oldName replaced by newName.
func renamedAliases(aliases []string, oldName, newName string) []string {
	if aliases == nil {
		return nil
	}
	renamed := make([]string, len(aliases))
	for i, alias := range aliases {
		if alias == oldName {
			alias = newName
		}
		renamed[i] = alias
	}
	return renamed
}
