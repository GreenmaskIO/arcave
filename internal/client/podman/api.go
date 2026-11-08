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

// Package podman reaches a Podman daemon where its Docker API falls short: an API answering
// as Docker's, over libpod's own, and the engine of package docker with a volume export of its own.
package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/docker/go-units"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
)

// mountAPIBindOption is the option libpod reports a bind of the mount API with; the bind
// API's carries rbind.
const mountAPIBindOption = "bind"

// logMaxSizeOption is the log option Docker keeps the size libpod reports as LogConfig.Size in.
const logMaxSizeOption = "max-size"

// errUnsupportedCommitOptions refuses what a commit through libpod would lose.
var errUnsupportedCommitOptions = errors.New("a commit through libpod takes no reference, author or config")

// errUnsupportedSaveOptions refuses a save libpod's one-image export cannot serve.
var errUnsupportedSaveOptions = errors.New("a save through libpod takes one image and no options")

// libpodAPI is what compat cannot do for API; *libpod.Client satisfies it.
type libpodAPI interface {
	InspectContainer(ctx context.Context, containerID string) (libpod.Container, error)
	CommitContainer(ctx context.Context, options libpod.CommitOptions) (string, error)
	ExportImage(ctx context.Context, imageID, format string) (io.ReadCloser, error)
}

// API is the Docker API of a Podman daemon, answering as Docker would: inspect, commit and save reach libpod.
type API struct {
	client.APIClient
	libpod libpodAPI
}

// NewAPI wraps api, connected to a Podman daemon, with libpod over the same connection.
func NewAPI(api client.APIClient, libpod libpodAPI) *API {
	return &API{
		APIClient: api,
		libpod:    libpod,
	}
}

// ContainerInspect is the compat inspect, Raw included, with libpod's subpath and mount-API bind
// mounts as mount requests and its log size as the max-size option.
func (a *API) ContainerInspect(
	ctx context.Context, containerID string, options client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	inspect, err := a.APIClient.ContainerInspect(ctx, containerID, options)
	if err != nil {
		return client.ContainerInspectResult{}, err
	}
	stored, err := a.libpod.InspectContainer(ctx, containerID)
	if err != nil {
		return client.ContainerInspectResult{}, fmt.Errorf("inspect container %s through libpod: %w", containerID, err)
	}
	return client.ContainerInspectResult{
		Container: withLibpodRequest(inspect.Container, stored),
		Raw:       inspect.Raw,
	}, nil
}

// ContainerCommit commits a container's writable layer through libpod as OCI, whose id survives a
// save and a load; the comment is dropped, OCI carries none. The id is libpod's, a bare hex.
func (a *API) ContainerCommit(
	ctx context.Context, containerID string, options client.ContainerCommitOptions,
) (client.ContainerCommitResult, error) {
	if options.Reference != "" || options.Author != "" || options.Config != nil {
		return client.ContainerCommitResult{}, errUnsupportedCommitOptions
	}
	committed, err := a.libpod.CommitContainer(ctx, libpod.CommitOptions{
		Container: containerID,
		Format:    libpod.CommitFormatOCI,
		Pause:     !options.NoPause,
		Changes:   options.Changes,
	})
	if err != nil {
		return client.ContainerCommitResult{}, err
	}
	return client.ContainerCommitResult{ID: committed}, nil
}

// ImageSave streams one image out as an OCI archive, through libpod; the caller closes the stream.
func (a *API) ImageSave(
	ctx context.Context, imageIDs []string, opts ...client.ImageSaveOption,
) (client.ImageSaveResult, error) {
	if len(imageIDs) != 1 || len(opts) != 0 {
		return nil, errUnsupportedSaveOptions
	}
	return a.libpod.ExportImage(ctx, imageIDs[0], libpod.ExportFormatOCIArchive)
}

// withLibpodRequest is inspect with the mount requests and log size libpod reports, and
// without the binds those requests replace.
func withLibpodRequest(inspect container.InspectResponse, stored libpod.Container) container.InspectResponse {
	if inspect.HostConfig == nil {
		return inspect
	}
	requests := newMountRequests(stored.Mounts)
	hostConfig := *inspect.HostConfig
	hostConfig.Binds = withoutRequestedBinds(hostConfig.Binds, requests)
	hostConfig.Mounts = slices.Concat(hostConfig.Mounts, requests)
	hostConfig.LogConfig = withLogSize(hostConfig.LogConfig, stored.HostConfig.LogConfig.Size)
	inspect.HostConfig = &hostConfig
	return inspect
}

// newMountRequests are the mount requests compat drops: a volume at a subpath and a bind of the mount API.
func newMountRequests(mounts []libpod.Mount) []mount.Mount {
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

// withoutRequestedBinds is binds without an entry whose target a request covers: Docker repeats
// no mount request there, and Podman refuses a create that does. An entry naming no target stays.
func withoutRequestedBinds(binds []string, requests []mount.Mount) []string {
	return slices.DeleteFunc(slices.Clone(binds), func(bind string) bool {
		fields := strings.Split(bind, ":")
		return len(fields) > 1 && slices.ContainsFunc(requests, func(request mount.Mount) bool {
			return request.Target == fields[1]
		})
	})
}

// withLogSize is logConfig with the size libpod reports as its max-size, which compat
// drops; libpod reports none as a negative size.
func withLogSize(logConfig container.LogConfig, size string) container.LogConfig {
	if bytes, err := units.RAMInBytes(size); err != nil || bytes <= 0 {
		return logConfig
	}
	options := maps.Clone(logConfig.Config)
	if options == nil {
		options = map[string]string{}
	}
	options[logMaxSizeOption] = size
	logConfig.Config = options
	return logConfig
}
