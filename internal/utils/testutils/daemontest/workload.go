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

package daemontest

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// BusyboxImage is what the tests' services and helper containers run.
const BusyboxImage = "busybox"

// LayerMarker sits in a replica's writable layer, past every mount; VolumeMarker in the volume
// its replicas share. A rollback has to put both back, and only the first comes out of an image.
const (
	LayerMarker  = "/layer-marker"
	VolumeMarker = "/data/marker"
)

// ImageRollbackSpec is one service, its compose project %[1]s, scaled to %[2]d replicas, each
// writing to its own writable layer and to a volume the replicas share.
const ImageRollbackSpec = `
name: %[1]s
services:
  app:
    image: busybox
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: %[2]d
    volumes:
      - data:/data
volumes:
  data: {}
`

// Exec runs cmd in the container containerID and returns its output, failing t on a non-zero exit.
func Exec(t *testing.T, daemon client.APIClient, containerID string, cmd ...string) string {
	t.Helper()

	ctx := context.Background()
	created, err := daemon.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	require.NoError(t, err)

	attached, err := daemon.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	require.NoError(t, err)
	defer attached.Close()

	var out bytes.Buffer
	_, err = stdcopy.StdCopy(&out, &out, attached.Reader)
	require.NoError(t, err)

	inspect, err := daemon.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	require.NoError(t, err)
	require.Zero(t, inspect.ExitCode, out.String())
	return out.String()
}

// WriteMarkers writes content to LayerMarker and VolumeMarker in the container containerID.
func WriteMarkers(t *testing.T, daemon client.APIClient, containerID, content string) {
	t.Helper()

	Exec(t, daemon, containerID, "sh", "-c",
		fmt.Sprintf("printf %%s %s > %s && printf %%s %s > %s", content, LayerMarker, content, VolumeMarker))
}
