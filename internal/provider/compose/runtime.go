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

package compose

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
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/imagebackend/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/fullcopy"
)

// runtimeProfile is everything that differs by the engine behind the daemon.
type runtimeProfile struct {
	// api is the daemon's Docker API, answering as Docker would; introspection and engine read through it.
	api client.APIClient
	// engine is what a workflow reaches the daemon through, but for a volume's stream.
	engine *docker.Engine
	// volumes is how a volume leaves the daemon: engine itself, or Podman's export over it.
	volumes fullcopy.VolumeStreamer
	// version is the version the engine reports; minDaemonMajor judges it.
	version string
	// minDaemonMajor is the oldest version of the engine a workflow runs on; 0 requires none.
	minDaemonMajor int
	// assembler leaves out of a spec what the engine stamps by itself.
	assembler *composespec.Assembler
}

// readRuntimeProfile asks the daemon which engine it is and chooses the profile for it.
func readRuntimeProfile(ctx context.Context, daemon daemonConnection) (runtimeProfile, error) {
	runtime, info, err := readEngine(ctx, daemon.api)
	if err != nil {
		return runtimeProfile{}, err
	}
	return newRuntimeProfile(runtime, info, daemon.api, daemon.cli.DockerEndpoint())
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

// newRuntimeProfile chooses everything that differs by the engine behind the daemon; libpod
// is reached through api's own dialer, over TLS when the endpoint is.
func newRuntimeProfile(
	runtime model.ContainerRuntime, info system.Info, api client.APIClient,
	endpoint dockercontext.Endpoint,
) (runtimeProfile, error) {
	switch runtime.Kind {
	case model.ContainerRuntimePodman:
		if isHTTPHost(endpoint.Host) {
			return runtimeProfile{}, fmt.Errorf("docker endpoint %q: the Docker client cannot attach to a container over an http:// or https:// host, which a Podman volume export needs: name it tcp://", endpoint.Host)
		}
		libpodClient, err := libpod.NewClient(api, libpod.Options{Version: runtime.Version})
		if err != nil {
			return runtimeProfile{}, fmt.Errorf("reach libpod: %w", err)
		}
		podmanAPI := podman.NewAPI(api, libpodClient)
		engine := docker.NewEngine(podmanAPI)
		return runtimeProfile{
			api:            podmanAPI,
			engine:         engine,
			volumes:        podman.NewEngine(engine),
			version:        runtime.Version,
			minDaemonMajor: 0,
			assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
		}, nil
	default:
		if model.IsDockerDesktop(info) {
			api = dockerdesktop.NewAPI(api)
		}
		engine := docker.NewEngine(api)
		return runtimeProfile{
			api:            api,
			engine:         engine,
			volumes:        engine,
			version:        runtime.Version,
			minDaemonMajor: dedupcopy.MinDaemonMajor,
			assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
		}, nil
	}
}

// isHTTPHost reports whether host, which only a context can spell so, names http or https.
func isHTTPHost(host string) bool {
	return strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://")
}
