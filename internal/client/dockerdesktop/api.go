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

// Package dockerdesktop reaches a Docker Desktop daemon where its Docker API falls short: an
// inspect spelling every bind on its host path, as the daemon was asked for it.
package dockerdesktop

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// vmHostMount is where Docker Desktop's VM on macOS mounts the host's root; a VM's path under
// another root, as on Windows, is left as it is.
const vmHostMount = "/host_mnt"

// sourceLabelPrefixes open the labels Docker Desktop stamps for each bind it rewrites to its VM's
// path, <prefix><n>/Source and <prefix><n>/Target: binds/ for the bind API, mounts/ for the mount API.
var sourceLabelPrefixes = []string{
	"desktop.docker.io/binds/",
	"desktop.docker.io/mounts/",
}

// API is the Docker API of a Docker Desktop daemon, its inspect spelling binds on the host.
type API struct {
	client.APIClient
}

// NewAPI wraps api, connected to a Docker Desktop daemon.
func NewAPI(api client.APIClient) *API {
	return &API{APIClient: api}
}

// ContainerInspect is the inspect, Raw included, with each bind on its host path: the one Docker
// Desktop recorded in its labels, else the VM's path with its host mount cut off. Docker Desktop
// spells a bind back itself only while no mount of another type comes before it. The container
// list is read only for an inspect naming a VM's path.
func (a *API) ContainerInspect(
	ctx context.Context, containerID string, options client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	inspect, err := a.APIClient.ContainerInspect(ctx, containerID, options)
	if err != nil {
		return client.ContainerInspectResult{}, err
	}
	if !namesVMPath(inspect.Container) {
		return inspect, nil
	}
	labels, err := a.readListedLabels(ctx, inspect.Container.ID)
	if err != nil {
		return client.ContainerInspectResult{}, err
	}
	return client.ContainerInspectResult{
		Container: withHostSources(inspect.Container, parseHostSources(labels)),
		Raw:       inspect.Raw,
	}, nil
}

// namesVMPath reports whether any bind of inspect is on a path of Docker Desktop's VM.
func namesVMPath(inspect container.InspectResponse) bool {
	for _, mountPoint := range inspect.Mounts {
		if mountPoint.Type == mount.TypeBind && isVMPath(mountPoint.Source) {
			return true
		}
	}
	if inspect.HostConfig == nil {
		return false
	}
	for _, request := range inspect.HostConfig.Mounts {
		if request.Type == mount.TypeBind && isVMPath(request.Source) {
			return true
		}
	}
	for _, entry := range inspect.HostConfig.Binds {
		if source, _, _ := strings.Cut(entry, ":"); isVMPath(source) {
			return true
		}
	}
	return false
}

// isVMPath reports whether path lies under the host's root as Docker Desktop's VM mounts it.
func isVMPath(path string) bool {
	rest, ok := strings.CutPrefix(path, vmHostMount)
	return ok && strings.HasPrefix(rest, "/")
}

// readListedLabels are the container's labels as the container list shows them, Docker Desktop's
// own among them, which the inspect leaves out; none for a container gone since the inspect.
func (a *API) readListedLabels(ctx context.Context, containerID string) (map[string]string, error) {
	filter := make(client.Filters).Add("id", containerID)
	listed, err := a.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("list container %s: %w", containerID, err)
	}
	for _, summary := range listed.Items {
		if summary.ID == containerID {
			return summary.Labels, nil
		}
	}
	return map[string]string{}, nil
}

// parseHostSources maps a bind's target to the host path Docker Desktop recorded for it.
func parseHostSources(labels map[string]string) map[string]string {
	sources, targets := map[string]string{}, map[string]string{}
	for key, value := range labels {
		for _, prefix := range sourceLabelPrefixes {
			rest, ok := strings.CutPrefix(key, prefix)
			if !ok {
				continue
			}
			index, field, _ := strings.Cut(rest, "/")
			switch field {
			case "Source":
				sources[prefix+index] = value
			case "Target":
				targets[prefix+index] = value
			}
		}
	}
	byTarget := make(map[string]string, len(targets))
	for index, target := range targets {
		if source, ok := sources[index]; ok {
			byTarget[target] = source
		}
	}
	return byTarget
}

// withHostSources is inspect with each bind on its host path, in the bind and mount requests and
// in the mounts; the inspect it came from is left as it was.
func withHostSources(inspect container.InspectResponse, hostSources map[string]string) container.InspectResponse {
	hostSource := func(target, source string) string {
		if !isVMPath(source) {
			return source
		}
		if recorded, ok := hostSources[target]; ok {
			return recorded
		}
		return strings.TrimPrefix(source, vmHostMount)
	}

	mounts := slices.Clone(inspect.Mounts)
	for i := range mounts {
		if mounts[i].Type == mount.TypeBind {
			mounts[i].Source = hostSource(mounts[i].Destination, mounts[i].Source)
		}
	}
	inspect.Mounts = mounts
	if inspect.HostConfig == nil {
		return inspect
	}

	hostConfig := *inspect.HostConfig
	hostConfig.Mounts = slices.Clone(hostConfig.Mounts)
	for i := range hostConfig.Mounts {
		if hostConfig.Mounts[i].Type == mount.TypeBind {
			hostConfig.Mounts[i].Source = hostSource(hostConfig.Mounts[i].Target, hostConfig.Mounts[i].Source)
		}
	}
	hostConfig.Binds = slices.Clone(hostConfig.Binds)
	for i, entry := range hostConfig.Binds {
		// source:target[:options]; a VM's path holds no colon.
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) < 2 {
			continue
		}
		parts[0] = hostSource(parts[1], parts[0])
		hostConfig.Binds[i] = strings.Join(parts, ":")
	}
	inspect.HostConfig = &hostConfig
	return inspect
}
