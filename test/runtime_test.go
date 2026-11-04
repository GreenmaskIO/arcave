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
	"io"
	"slices"
	"testing"

	"github.com/distribution/reference"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
)

// exportEngine is how volumes and images leave the daemon: engine itself, or Podman's.
type exportEngine interface {
	fullcopy.VolumeStreamer
	CommitContainer(ctx context.Context, containerID, comment string, labels map[string]string) (model.ImageDigestID, error)
	SaveImage(ctx context.Context, imageID model.ImageDigestID) (io.ReadCloser, error)
	RemoveImage(ctx context.Context, imageID model.ImageDigestID) error
	LoadImage(ctx context.Context, r io.Reader) error
	TagImage(ctx context.Context, imageID model.ImageDigestID, ref string) error
	ImageExists(ctx context.Context, imageID model.ImageDigestID) (bool, error)
}

// runtimeProfile is what arcave's DI root chooses by the engine behind the daemon.
type runtimeProfile struct {
	runtime model.ContainerRuntimeKind
	// api is what a compose project is introspected through.
	api       client.APIClient
	exporter  exportEngine
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
	engine := dockerclient.NewEngine(api)
	if runtime.Kind == model.ContainerRuntimePodman {
		libpod := dockerclient.NewLibpod(api)
		return runtimeProfile{
			runtime:   runtime.Kind,
			api:       dockerclient.NewPodmanAPI(api, libpod),
			exporter:  dockerclient.NewPodmanEngine(engine, libpod),
			assembler: composespec.NewAssembler(composespec.NewPodmanDefaults(info)),
		}
	}
	return runtimeProfile{
		runtime:   runtime.Kind,
		api:       api,
		exporter:  engine,
		assembler: composespec.NewAssembler(composespec.NewDockerDefaults(info)),
	}
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
