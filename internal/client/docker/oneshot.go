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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// attaches reports whether the run exchanges any stdio with the container.
func attaches(spec model.OneShotSpec) bool {
	return spec.Stdin != nil || spec.Stdout != nil || spec.Stderr != nil
}

// runOneShotContainer drives one throwaway container through its whole
// lifecycle: ensure the image is present, create, attach (when the spec carries
// stdio), start, exchange, wait for exit, and always remove it. A non-zero exit
// returns an error carrying the container's own output, the way the CLI's
// combined output used to.
//
// This is the mechanism only — which image to run and what to mount it with is
// the caller's policy: wipeMount's in this package, a backend's through
// Engine.RunOneShot.
func runOneShotContainer(ctx context.Context, api dockerAPI, spec model.OneShotSpec) error {
	if err := ensureImage(ctx, api, spec.Image); err != nil {
		return err
	}

	cfg := &container.Config{
		Image:        spec.Image,
		Cmd:          spec.Cmd,
		Env:          spec.Env,
		OpenStdin:    spec.Stdin != nil,
		StdinOnce:    spec.Stdin != nil,
		AttachStdin:  spec.Stdin != nil,
		AttachStdout: spec.Stdout != nil,
		AttachStderr: spec.Stderr != nil,
		// Tty stays false even with stdio: the framed stream is what lets stdout
		// and stderr be told apart on the way back.
	}
	hostCfg := &container.HostConfig{Binds: spec.Binds}
	if spec.HostNetwork {
		hostCfg.NetworkMode = network.NetworkHost
	}
	created, err := api.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create %s container: %w", spec.Image, err)
	}
	defer func() {
		// The removal must outlive a cancelled run: on the run's own ctx the daemon
		// would refuse it, leaving the container running with nobody to stop it.
		if err := api.ContainerRemove(
			context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true},
		); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("container", created.ID).Str("image", spec.Image).
				Msg("remove one-shot container")
		}
	}()

	// The attach happens before the start, not after: a container started first
	// can produce — and lose — output before anyone is listening.
	var attached types.HijackedResponse
	if attaches(spec) {
		attached, err = api.ContainerAttach(ctx, created.ID, container.AttachOptions{
			Stream: true,
			Stdin:  spec.Stdin != nil,
			Stdout: spec.Stdout != nil,
			Stderr: spec.Stderr != nil,
		})
		if err != nil {
			return fmt.Errorf("attach to %s container: %w", spec.Image, err)
		}
		defer attached.Close()
		// Nothing in the copy watches ctx, so cancellation has to break the raw
		// connection by hand — a container that goes quiet outlives any deadline.
		stopOnCancel := context.AfterFunc(ctx, attached.Close)
		defer stopOnCancel()
	}

	if err := api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start %s container: %w", spec.Image, err)
	}

	if attaches(spec) {
		if err := exchange(ctx, attached, spec); err != nil {
			return err
		}
	}

	statusCh, errCh := api.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait for %s container: %w", spec.Image, err)
	case status := <-statusCh:
		if status.StatusCode != 0 {
			output := containerOutput(ctx, api, created.ID)
			zerolog.Ctx(ctx).Debug().
				Str("container", created.ID).
				Str("image", spec.Image).
				Strs("container_output", output).
				Msg("one-shot container exited non-zero")
			return &model.ContainerExitError{
				Image:      spec.Image,
				StatusCode: status.StatusCode,
				Output:     output,
			}
		}
	}
	return nil
}

// scratchCmd is what the scratch container runs: it exists to hold the volume,
// so it exits at once and is only ever copied in and out of.
var scratchCmd = []string{"true"}

// withScratchContainer runs fn against a throwaway container with src mounted
// at scratchMount, and removes it when fn returns.
func withScratchContainer(
	ctx context.Context, api dockerAPI, src model.MountSource, fn func(id string) error,
) error {
	id, remove, err := startScratchContainer(ctx, api, src)
	if err != nil {
		return err
	}
	defer remove()

	return fn(id)
}

// startScratchContainer creates a container holding src at scratchMount, runs
// it to exit, and returns its id together with its removal. The caller runs the
// removal — a stream copied out of the container has to outlive the call that
// opened it.
//
// The run to exit is not ceremony: the daemon populates a volume when the
// container that mounts it starts, so copying before that would read a mount
// nothing has set up yet.
func startScratchContainer(
	ctx context.Context, api dockerAPI, src model.MountSource,
) (id string, remove func(), err error) {
	if src.Source == "" {
		return "", nil, fmt.Errorf("cannot reach a %q volume with no source", src.Type)
	}
	if err := ensureImage(ctx, api, scratchImage); err != nil {
		return "", nil, err
	}

	created, err := api.ContainerCreate(ctx,
		&container.Config{Image: scratchImage, Cmd: scratchCmd},
		&container.HostConfig{Binds: []string{src.Source + ":" + scratchMount}},
		nil, nil, "")
	if err != nil {
		return "", nil, fmt.Errorf("create %s container: %w", scratchImage, err)
	}
	remove = func() {
		// The removal must outlive a cancelled run: on the run's own ctx the daemon
		// would refuse it, leaving the container behind with nobody to remove it.
		if err := api.ContainerRemove(
			context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true},
		); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("container", created.ID).Str("image", scratchImage).
				Msg("remove scratch container")
		}
	}

	if err := api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		remove()
		return "", nil, fmt.Errorf("start %s container: %w", scratchImage, err)
	}
	statusCh, errCh := api.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		remove()
		return "", nil, fmt.Errorf("wait for %s container: %w", scratchImage, err)
	case <-statusCh:
	}
	return created.ID, remove, nil
}

// ensureImage pulls ref unless the daemon already has it. The pull stream is
// drained rather than reported: the daemon only completes the pull once the
// stream is consumed, and arcave has no progress UI to render it into.
func ensureImage(ctx context.Context, api dockerAPI, ref string) error {
	if _, err := api.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", ref, err)
	}

	rc, err := api.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer func() {
		if err := rc.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("image", ref).Msg("close image pull stream")
		}
	}()

	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	return nil
}

// exchange pumps the caller's streams through the hijacked connection while the
// container runs, and returns once both directions are done.
//
// The writer and the reader are concurrent on purpose: filling stdin before
// reading any stdout deadlocks on any payload larger than the pipe buffer — the
// container blocks writing output nobody drains, arcave blocks writing input nobody
// reads, and neither ever gets to the other half.
func exchange(ctx context.Context, attached types.HijackedResponse, spec model.OneShotSpec) error {
	var g errgroup.Group

	if spec.Stdin != nil {
		g.Go(func() error {
			src := &recordingReader{r: spec.Stdin}
			// A container that exits without draining its stdin (think `head`) fails
			// this copy on the *write* side, which is not an error: the exit status
			// decides that run's outcome. A failure to read the caller's own source
			// is an error, and is the only one reported here.
			if _, err := io.Copy(attached.Conn, src); err != nil && src.err != nil {
				return fmt.Errorf("read %s container input: %w", spec.Image, src.err)
			}
			// Half-close so the process sees EOF on stdin rather than hanging.
			if err := attached.CloseWrite(); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Str("image", spec.Image).
					Msg("half-close one-shot container stdin")
			}
			return nil
		})
	}

	g.Go(func() error {
		stdout, stderr := spec.Stdout, spec.Stderr
		if stdout == nil {
			stdout = io.Discard
		}
		if stderr == nil {
			stderr = io.Discard
		}
		if _, err := stdcopy.StdCopy(stdout, stderr, attached.Reader); err != nil {
			return fmt.Errorf("read %s container output: %w", spec.Image, err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		// A cancelled run breaks its own connection; report the cause, not the symptom.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s container run cancelled: %w", spec.Image, ctxErr)
		}
		return err
	}
	return nil
}

// recordingReader remembers the error its source returned, so a failed io.Copy
// can be attributed to the read side rather than to the write side.
type recordingReader struct {
	r   io.Reader
	err error
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		rr.err = err
	}
	return n, err
}

// maxContainerOutputBytes bounds how much of a failed container's log is read
// back into an error: enough to diagnose the failure, not enough to flood a JSON
// result or the context of an agent reading an MCP tool result. Tail already
// caps the line count at the daemon; this caps the bytes a few very long lines
// could still add.
const maxContainerOutputBytes = 8 << 10 // 8 KiB

// containerOutput reads back what a failed container printed — bounded and
// demultiplexed — so the exit error carries the daemon's own message the way the
// CLI's combined output used to. It is best-effort: a failure to read the logs
// must not replace the exit status being reported, so a problem comes back as a
// single explanatory line rather than an error.
func containerOutput(ctx context.Context, api dockerAPI, containerID string) []string {
	// Tail bounds the read at the daemon. Tty is false for every one-shot run, so
	// the stream is stdcopy-framed (an 8-byte header per frame) and must be
	// demultiplexed — reading it raw would put the framing bytes into the message.
	rc, err := api.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "200",
	})
	if err != nil {
		return []string{"(logs unavailable: " + err.Error() + ")"}
	}
	defer func() {
		if err := rc.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("container", containerID).Msg("close container logs")
		}
	}()

	// StdCopy writes one frame at a time on this goroutine, so a single buffer for
	// both streams is safe; the error text does not need to tell stdout from stderr.
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, io.LimitReader(rc, maxContainerOutputBytes)); err != nil {
		return []string{"(logs unavailable: " + err.Error() + ")"}
	}

	trimmed := strings.TrimSpace(buf.String())
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}
