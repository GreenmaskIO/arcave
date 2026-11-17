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
	"slices"
	"testing"

	"github.com/distribution/reference"
	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	dockerclient "github.com/greenmaskio/arcave/internal/provider/compose/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/runtimeprofile"
)

// newRuntimeProfile is the profile the compose provider chooses for the daemon api answers as.
func newRuntimeProfile(t *testing.T, api *client.Client) runtimeprofile.Profile {
	t.Helper()
	profile, err := runtimeprofile.Read(context.Background(), api,
		dockercontext.Endpoint{EndpointMeta: dockercontext.EndpointMeta{Host: api.DaemonHost()}})
	require.NoError(t, err)
	return profile
}

// newLibpodClient reaches libpod at api's daemon, Podman of version, through api's own dialer.
func newLibpodClient(t *testing.T, api *client.Client, version string) *libpod.Client {
	t.Helper()
	libpodClient, err := libpod.NewClient(api, libpod.Options{Version: version})
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
