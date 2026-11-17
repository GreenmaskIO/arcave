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

// Package runtimeprofile chooses everything that differs by the engine behind the daemon: the one
// switch on model.ContainerRuntimeKind below model. It is the provider root's, the clients it builds
// with; no service imports it.
package runtimeprofile

import (
	"context"
	"fmt"
	"strings"

	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/client/dockerdesktop"
	"github.com/greenmaskio/arcave/internal/client/podman"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/imagebackend/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/fullcopy"
)

// Profile is everything that differs by the engine behind the daemon.
type Profile struct {
	// Kind is the engine.
	Kind model.ContainerRuntimeKind
	// API is the daemon's Docker API, answering as Docker would; introspection and Engine read through it.
	API client.APIClient
	// Engine is what a workflow reaches the daemon through, but for a volume's stream.
	Engine *docker.Engine
	// Volumes is how a volume leaves the daemon: Engine itself, or Podman's export over it.
	Volumes fullcopy.VolumeStreamer
	// Version is the version the engine reports; MinDaemonMajor judges it.
	Version string
	// MinDaemonMajor is the oldest version of the engine a workflow runs on; 0 requires none.
	MinDaemonMajor int
	// Assembler leaves out of a spec what the engine stamps by itself.
	Assembler *composespec.Assembler
}

// Read asks the daemon api answers for which engine it is and chooses the profile for it; endpoint
// is where api reaches it.
func Read(ctx context.Context, api client.APIClient, endpoint dockercontext.Endpoint) (Profile, error) {
	runtime, info, err := readEngine(ctx, api)
	if err != nil {
		return Profile{}, err
	}
	return New(runtime, info, api, endpoint)
}

// New chooses everything that differs by the engine behind the daemon; libpod is reached through
// api's own dialer, over TLS when the endpoint is.
func New(
	runtime model.ContainerRuntime, info system.Info, api client.APIClient, endpoint dockercontext.Endpoint,
) (Profile, error) {
	switch runtime.Kind {
	case model.ContainerRuntimePodman:
		if isHTTPHost(endpoint.Host) {
			return Profile{}, fmt.Errorf("docker endpoint %q: the Docker client cannot attach to a container over an http:// or https:// host, which a Podman volume export needs: name it tcp://", endpoint.Host)
		}
		libpodClient, err := libpod.NewClient(api, libpod.Options{Version: runtime.Version})
		if err != nil {
			return Profile{}, fmt.Errorf("reach libpod: %w", err)
		}
		podmanAPI := podman.NewAPI(api, libpodClient)
		engine := docker.NewEngine(podmanAPI)
		return Profile{
			Kind:           runtime.Kind,
			API:            podmanAPI,
			Engine:         engine,
			Volumes:        podman.NewEngine(engine),
			Version:        runtime.Version,
			MinDaemonMajor: 0,
			Assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
		}, nil
	default:
		if model.IsDockerDesktop(info) {
			api = dockerdesktop.NewAPI(api)
		}
		engine := docker.NewEngine(api)
		return Profile{
			Kind:           runtime.Kind,
			API:            api,
			Engine:         engine,
			Volumes:        engine,
			Version:        runtime.Version,
			MinDaemonMajor: dedupcopy.MinDaemonMajor,
			Assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
		}, nil
	}
}

// readEngine asks the daemon which engine it is and what `docker info` says of it.
func readEngine(ctx context.Context, api client.APIClient) (model.ContainerRuntime, system.Info, error) {
	runtime, err := docker.ReadRuntime(ctx, api)
	if err != nil {
		return model.ContainerRuntime{}, system.Info{}, err
	}
	info, err := api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return model.ContainerRuntime{}, system.Info{}, fmt.Errorf("read the daemon info: %w", err)
	}
	return runtime, info.Info, nil
}

// isHTTPHost reports whether host, which only a context can spell so, names http or https.
func isHTTPHost(host string) bool {
	return strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://")
}
