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
	"os"
	"slices"
	"testing"

	"github.com/distribution/reference"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	podmanclient "github.com/greenmaskio/arcave/internal/client/podman"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
)

// runtimeProfile is what arcave's DI root chooses by the engine behind the daemon.
type runtimeProfile struct {
	runtime model.ContainerRuntimeKind
	// api is the daemon's Docker API, answering as Docker would.
	api client.APIClient
	// engine is what a workflow reaches the daemon through, over api.
	engine *dockerclient.Engine
	// volumes is how a volume leaves the daemon.
	volumes   fullcopy.VolumeStreamer
	assembler *composespec.Assembler
}

// newRuntimeProfile is the profile arcave's DI root builds for the daemon api answers as. It
// mirrors cmd/arcave's newRuntimeProfile, which package main cannot export.
func newRuntimeProfile(t *testing.T, api *client.Client) runtimeProfile {
	t.Helper()
	runtime, err := dockerclient.ReadRuntime(context.Background(), api)
	require.NoError(t, err)
	info, err := api.Info(context.Background())
	require.NoError(t, err)
	if runtime.Kind == model.ContainerRuntimePodman {
		libpodClient := newLibpodClient(t, api, runtime.Version)
		podmanAPI := podmanclient.NewAPI(api, libpodClient)
		engine := dockerclient.NewEngine(podmanAPI)
		return runtimeProfile{
			runtime:   runtime.Kind,
			api:       podmanAPI,
			engine:    engine,
			volumes:   podmanclient.NewEngine(engine),
			assembler: composespec.NewAssembler(composespec.NewPodmanStamps(info)),
		}
	}
	engine := dockerclient.NewEngine(api)
	return runtimeProfile{
		runtime:   runtime.Kind,
		api:       api,
		engine:    engine,
		volumes:   engine,
		assembler: composespec.NewAssembler(composespec.NewDockerStamps(info)),
	}
}

// newLibpodClient reaches libpod over api, whose daemon is Podman of version.
func newLibpodClient(t *testing.T, api *client.Client, version string) *libpod.Client {
	t.Helper()
	libpodClient, err := libpod.NewClient(api, libpod.Options{
		Version: version,
		UseTLS:  os.Getenv(client.EnvOverrideCertPath) != "",
	})
	require.NoError(t, err)
	return libpodClient
}

// isPodman reports whether api's daemon is Podman.
func isPodman(t *testing.T, api *client.Client) bool {
	t.Helper()
	runtime, err := dockerclient.ReadRuntime(context.Background(), api)
	require.NoError(t, err)
	return runtime.Kind == model.ContainerRuntimePodman
}

// sameImageRef reports whether two refs name one image once normalized, the way Docker
// fills in docker.io/library/ and :latest: Podman reports busybox as busybox:latest.
func sameImageRef(want, got string) bool {
	normalize := func(ref string) string {
		named, err := reference.ParseNormalizedNamed(ref)
		if err != nil {
			return ref
		}
		return reference.TagNameOnly(named).String()
	}
	return normalize(want) == normalize(got)
}

// assertSameImageRef asserts got names the image want does.
func assertSameImageRef(t *testing.T, want, got string) {
	t.Helper()
	assert.True(t, sameImageRef(want, got), "%q does not name %q", got, want)
}

// assertNamesImage asserts some ref of refs names the image want does.
func assertNamesImage(t *testing.T, refs []string, want string) {
	t.Helper()
	assert.True(t, slices.ContainsFunc(refs, func(ref string) bool { return sameImageRef(want, ref) }),
		"%q names no %q", refs, want)
}
