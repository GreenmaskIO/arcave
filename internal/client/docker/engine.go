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

// Package docker acts on the *running* compose project, in contrast to internal/client/
// compose, which only reads what the compose files declare. It holds the two
// types arcave needs to reach the daemon, split by what they act on:
//
//   - Engine (engine.go) moves a volume's bytes in and out of the volume
//     itself, through a throwaway container of its own.
//   - ComposeRunner (loader.go) stops and starts the project's services,
//     through the compose library (github.com/docker/compose/v2), since the
//     Docker API itself has no Compose equivalent.
//
// Both reach the daemon through Go libraries — arcave shells out to nothing, so it
// needs no docker CLI or compose plugin installed, only a reachable daemon.
// They share one client: Engine owns the docker/cli instance that resolves
// Options (host, context, TLS) into a connection, and ComposeRunner is built
// over that same instance. Targeting one daemon is therefore a property of the
// construction rather than a contract two halves have to honour by hand, and
// that shared client is why the two live in one package.
package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/greenmaskio/arcave/internal/model"
)

// composeProjectLabel and composeServiceLabel are set by Docker Compose on
// every container it creates.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
	composeNumberLabel  = "com.docker.compose.container-number"
)

// scratchImage is the throwaway image every volume operation runs: small,
// ubiquitous, and carrying the `find` the wipe needs.
const scratchImage = "busybox"

// scratchMount is where a volume is mounted inside that container. It is absent
// from the image on purpose: starting a container with an empty volume mounted
// at a path the image *does* carry copies the image's content into the volume.
const scratchMount = "/arcave-data"

// wipeMountPath is where the volume sits inside the wipe container. Absent from
// the image for the same reason as scratchMount.
const wipeMountPath = "/data"

// dockerAPI is the slice of the Docker API client this package actually calls —
// consumer-side, so a test can drive Engine with a mock instead of a daemon.
// *client.Client (and so docker/cli's client.APIClient) satisfies it.
type dockerAPI interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerAttach(ctx context.Context, containerID string, options container.AttachOptions) (types.HijackedResponse, error)
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error)
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options container.CopyToContainerOptions) error
	ContainerCommit(ctx context.Context, containerID string, options container.CommitOptions) (container.CommitResponse, error)
	ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	ImageSave(ctx context.Context, imageIDs []string, opts ...client.ImageSaveOption) (io.ReadCloser, error)
	ImageLoad(ctx context.Context, input io.Reader, opts ...client.ImageLoadOption) (image.LoadResponse, error)
	ImageTag(ctx context.Context, source, target string) error
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
	Info(ctx context.Context) (system.Info, error)
	VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error)
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
	Close() error
}

// Engine is arcave's volume I/O against the Docker daemon: it streams a
// volume's contents out (OpenVolume) and wipes-and-extracts a stream back in
// (WriteVolume), through a throwaway container of its own rather than through
// whichever service happens to mount the volume. These are the operations that
// have no Compose-level equivalent, so they go through the Docker API — see
// ComposeRunner for the lifecycle half.
//
// It does not resolve that connection: the composition root builds one client
// from the config and hands the same one to Engine and to the compose service
// ComposeRunner drives, so targeting a single daemon is a property of the
// construction rather than a contract this package has to keep. Resolution lives
// there because it is where a failure can still be reported — docker/cli answers
// an unresolvable endpoint with os.Exit(1).
//
// One Engine is created per command at the DI root and injected into both
// snapshot backends; it is concurrency-safe, and its Close belongs to whoever
// created it.
type Engine struct {
	// api is the resolved daemon connection. A test sets it directly, standing in
	// for the client the composition root would have built.
	api dockerAPI
}

// NewEngine wraps an already-resolved daemon connection.
func NewEngine(api dockerAPI) *Engine {
	return &Engine{api: api}
}

// Close releases the daemon connection.
func (e *Engine) Close() error { return e.api.Close() }

// RunOneShot drives one throwaway container to completion; a non-zero exit is a
// model.ContainerExitError carrying the container's own output. It is the
// exported face of the mechanism in oneshot.go, for the backends that run their
// own containers.
func (e *Engine) RunOneShot(ctx context.Context, spec model.OneShotSpec) error {
	return runOneShotContainer(ctx, e.api, spec)
}

// ResolveMountSource answers what a container binds to reach volume's data. The
// compose project alone answers for a named volume and a bind; only an
// anonymous volume — whose docker name is created per container — has to be
// looked up through the service that mounts it.
//
// A named volume that does not exist is an error here, before anything creates
// it: `docker create -v name:/x` would create the volume itself, so an empty
// capture would silently replace live data at the next rollback.
func (e *Engine) ResolveMountSource(ctx context.Context, volume model.Volume) (model.MountSource, error) {
	if src, ok := volume.MountSource(); ok {
		if src.Type == model.MountSourceVolume {
			if _, err := e.api.VolumeInspect(ctx, src.Source); err != nil {
				return model.MountSource{}, fmt.Errorf(
					"docker volume %q does not exist (bring the project up once to create it): %w", src.Source, err)
			}
		}
		return src, nil
	}

	service, replica, target, ok := volume.LookupMount()
	if !ok {
		return model.MountSource{}, fmt.Errorf(
			"volume %s of type %s carries no way to reach its data", volume.ObjectID, volume.Type)
	}
	// An anonymous volume exists only as long as the container it was created
	// for, so this is the one case a missing container is fatal.
	mount, err := findServiceMount(ctx, e.api, volume.ComposeProject, service, replica, target)
	if err != nil {
		return model.MountSource{}, err
	}
	return mountSourceOf(mount)
}

// OpenVolume streams the whole volume out as a tar, through a throwaway
// container that mounts it at scratchMount. The archive is streamed verbatim
// rather than extracted so that ownership and permissions — which an
// unprivileged host process could not reapply — survive a later restore.
//
// The caller owns the returned stream: closing it removes the container, which
// therefore outlives this call.
func (e *Engine) OpenVolume(ctx context.Context, src model.MountSource) (io.ReadCloser, error) {
	id, remove, err := startScratchContainer(ctx, e.api, src)
	if err != nil {
		return nil, err
	}

	rc, _, err := e.api.CopyFromContainer(ctx, id, scratchMount)
	if err != nil {
		remove()
		return nil, fmt.Errorf("export volume %s: %w", src.Source, err)
	}
	return closeAfter{ReadCloser: rc, after: remove}, nil
}

// WriteVolume clears the volume and extracts the tar back into it, the mirror of
// OpenVolume. The archive's top entry is scratchMount's basename, so it is
// extracted at the container's root and the data lands back in the volume.
func (e *Engine) WriteVolume(ctx context.Context, src model.MountSource, r io.Reader) error {
	if err := e.WipeVolume(ctx, src); err != nil {
		return fmt.Errorf("clear volume %s before restore: %w", src.Source, err)
	}

	return withScratchContainer(ctx, e.api, src, func(id string) error {
		// readOnly hides r's Close from the SDK: CopyToContainer passes the stream to
		// net/http as the request body, and http.NewRequestWithContext type-asserts it
		// to io.ReadCloser — so the transport would close the caller's stream, and the
		// caller's own deferred Close would then fail with "file already closed".
		if err := e.api.CopyToContainer(ctx, id, "/", readOnly{r}, container.CopyToContainerOptions{}); err != nil {
			return fmt.Errorf("restore volume %s: %w", src.Source, err)
		}
		return nil
	})
}

// WipeVolume clears the volume's contents — WriteVolume's first half on its own,
// for a backend that writes the data back itself.
func (e *Engine) WipeVolume(ctx context.Context, src model.MountSource) error {
	if src.Source == "" {
		return fmt.Errorf("cannot clear a %q volume with no source", src.Type)
	}
	return wipeVolume(ctx, e.api, src)
}

// RemoveVolume drops one docker volume by name. Not forced: the daemon's refusal
// is what tells a still-mounted volume from the orphan the caller meant.
func (e *Engine) RemoveVolume(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("cannot remove a volume with no name")
	}
	if err := e.api.VolumeRemove(ctx, name, false); err != nil {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

// closeAfter runs after once the stream it wraps is closed — how a container
// outlives the call that opened its stream.
type closeAfter struct {
	io.ReadCloser
	after func()
}

func (c closeAfter) Close() error {
	defer c.after()
	return c.ReadCloser.Close()
}

// mountInfo carries the backing-storage details of a resolved container mount,
// used to clear it before a restore.
type mountInfo struct {
	Type   string // "volume" or "bind"
	Name   string // docker volume name (volume mounts)
	Source string // host path (bind mounts)
}

// findServiceMount returns the backing-storage details of the mount at target in the
// project's container for service. Stopped containers are included, since export
// and restore run while services are down.
func findServiceMount(
	ctx context.Context, api dockerAPI, projectName, service string, replica int, target string,
) (mountInfo, error) {
	filter := filters.NewArgs(
		filters.Arg("label", composeProjectLabel+"="+projectName),
		filters.Arg("label", composeServiceLabel+"="+service),
	)
	containers, err := api.ContainerList(ctx, container.ListOptions{All: true, Filters: filter})
	if err != nil {
		return mountInfo{}, fmt.Errorf("list containers for service %q: %w", service, err)
	}
	mount, ok := matchMount(containers, replica, target)
	if !ok {
		return mountInfo{}, fmt.Errorf("no container for service %q replica %d in project %q mounts %q (is the project created?)",
			service, replica, projectName, target)
	}
	return mount, nil
}

// matchMount returns the details of the mount at target in the container that mounts it;
// a non-zero replica admits only that container. ok is false when none does.
func matchMount(containers []container.Summary, replica int, target string) (mountInfo, bool) {
	for _, summary := range containers {
		if replica != 0 && summary.Labels[composeNumberLabel] != strconv.Itoa(replica) {
			continue
		}
		for _, mount := range summary.Mounts {
			if mount.Destination == target {
				return mountInfo{Type: string(mount.Type), Name: mount.Name, Source: mount.Source}, true
			}
		}
	}
	return mountInfo{}, false
}

// readOnly wraps a reader so that no io.Reader method set other than Read is
// visible, even when the underlying value also implements io.Closer.
type readOnly struct{ r io.Reader }

func (ro readOnly) Read(p []byte) (int, error) { return ro.r.Read(p) }

// wipeVolume removes every entry of a volume by running a throwaway root
// container that mounts it. This is the only host-agnostic way to delete data a
// service wrote as root: the daemon owns those files, and CopyToContainer only
// merges entries, never deletes them.
//
// It is the wipe *policy* — which image, which command, which bind — over the
// reusable one-shot container mechanism in oneshot.go, and exchanges no stdio.
func wipeVolume(ctx context.Context, api dockerAPI, src model.MountSource) error {
	return runOneShotContainer(ctx, api, model.OneShotSpec{
		Image: scratchImage,
		// find -mindepth 1 clears hidden entries too; -exec ... + batches the removal.
		Cmd: []string{"find", wipeMountPath, "-mindepth", "1", "-maxdepth", "1",
			"-exec", "rm", "-rf", "{}", "+"},
		Binds: []string{src.Source + ":" + wipeMountPath},
	})
}

// mountSourceOf is what reaches a resolved mount's backing storage: volumes by
// volume name, bind mounts by host path. The one place a daemon-side mount
// becomes a MountSource, so what a lookup answers cannot disagree with what the
// compose project answers directly.
func mountSourceOf(mount mountInfo) (model.MountSource, error) {
	var src model.MountSource
	switch mount.Type {
	case "volume":
		src = model.MountSource{Type: model.MountSourceVolume, Source: mount.Name}
	case "bind":
		src = model.MountSource{Type: model.MountSourceBind, Source: mount.Source}
	default:
		return model.MountSource{}, fmt.Errorf("unsupported mount type %q", mount.Type)
	}
	if src.Source == "" {
		return model.MountSource{}, fmt.Errorf("cannot resolve backing storage for %q mount", mount.Type)
	}
	return src, nil
}
