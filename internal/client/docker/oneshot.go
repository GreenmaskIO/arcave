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
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// oneShotLabel marks every throwaway container arcave creates, so one a killed run
// left behind can be found: `docker ps -a --filter label=arcave.oneshot`.
const oneShotLabel = "arcave.oneshot"

// newOneShotLabels is the label set of a throwaway container.
func newOneShotLabels() map[string]string {
	return map[string]string{oneShotLabel: "true"}
}

// attaches reports whether the run exchanges any stdio with the container.
func attaches(spec model.OneShotSpec) bool {
	return spec.Stdin != nil || spec.Stdout != nil || spec.Stderr != nil
}

// runOneShotContainer runs one throwaway container to exit, its spec.Files
// copied in before it starts and spec.Exec served while it runs, and always
// removes it. A non-zero exit, carrying the container's output, outranks an exec
// that could not start.
func runOneShotContainer(ctx context.Context, api dockerAPI, spec model.OneShotSpec) error {
	if err := ensureImage(ctx, api, spec.Image); err != nil {
		return err
	}
	cfg, hostCfg := newOneShotConfig(spec)
	created, err := api.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg})
	if err != nil {
		return fmt.Errorf("create %s container: %w", spec.Image, err)
	}
	defer removeOneShotContainer(ctx, api, created.ID, spec.Image, "one-shot")

	if len(spec.Files) > 0 {
		if err := copyOneShotFiles(ctx, api, created.ID, spec.Files); err != nil {
			return err
		}
	}

	// The attach happens before the start, not after: a container started first
	// can produce — and lose — output before anyone is listening.
	var attached client.HijackedResponse
	if attaches(spec) {
		if attached, err = attachOneShotContainer(ctx, api, created.ID, spec); err != nil {
			return err
		}
		defer attached.Close()
		// Nothing in the copy watches ctx, so cancellation has to break the raw
		// connection by hand — a container that goes quiet outlives any deadline.
		stopOnCancel := context.AfterFunc(ctx, attached.Close)
		defer stopOnCancel()
	}

	if _, err := api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start %s container: %w", spec.Image, err)
	}

	// An exec that could not start still lets an exited container's stdio be read:
	// its stderr is what says why it exited.
	var execErr error
	if spec.Exec != nil {
		stopExec, err := serveExec(ctx, api, created.ID, spec)
		switch {
		case err != nil && isContainerRunning(ctx, api, created.ID):
			// A container waiting on its exec's peer would never exit.
			if event := zerolog.Ctx(ctx).Debug(); event.Enabled() {
				event.Err(err).Str("container", created.ID).Str("image", spec.Image).
					Strs("container_output", containerOutput(ctx, api, created.ID)).
					Msg("one-shot container exec not started")
			}
			return fmt.Errorf("serve %s container: %w", spec.Image, err)
		case err != nil:
			execErr = err
		default:
			defer stopExec()
		}
	}

	if attaches(spec) {
		if err := exchange(ctx, attached, spec); err != nil {
			return err
		}
	}
	if err := waitOneShotContainer(ctx, api, created.ID, spec.Image); err != nil {
		if execErr != nil {
			zerolog.Ctx(ctx).Debug().Err(execErr).Str("container", created.ID).Str("image", spec.Image).
				Msg("one-shot container exec not started")
		}
		return err
	}
	if execErr != nil {
		return fmt.Errorf("serve %s container: %w", spec.Image, execErr)
	}
	return nil
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

	rc, err := api.ImagePull(ctx, ref, client.ImagePullOptions{})
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

// newOneShotConfig is the container and host config spec runs under.
func newOneShotConfig(spec model.OneShotSpec) (*container.Config, *container.HostConfig) {
	cfg := &container.Config{
		Image:        spec.Image,
		Entrypoint:   spec.Entrypoint,
		Cmd:          spec.Cmd,
		Env:          spec.Env,
		Labels:       newOneShotLabels(),
		OpenStdin:    spec.Stdin != nil,
		StdinOnce:    spec.Stdin != nil,
		AttachStdin:  spec.Stdin != nil,
		AttachStdout: spec.Stdout != nil,
		AttachStderr: spec.Stderr != nil,
		// Tty stays false even with stdio: the framed stream is what lets stdout
		// and stderr be told apart on the way back.
	}
	var (
		networkMode container.NetworkMode
		logConfig   container.LogConfig
	)
	if spec.HostNetwork {
		networkMode = network.NetworkHost
	}
	if spec.DiscardLog {
		logConfig = container.LogConfig{Type: "none"}
	}
	hostCfg := &container.HostConfig{
		Binds:       spec.Binds,
		SecurityOpt: spec.SecurityOpt,
		NetworkMode: networkMode,
		LogConfig:   logConfig,
	}
	return cfg, hostCfg
}

// removeOneShotContainer force-removes containerID, logging a failure under its
// role. The removal outlives a cancelled run: on the run's own ctx the daemon
// would refuse it, leaving the container behind with nobody to remove it.
func removeOneShotContainer(ctx context.Context, api dockerAPI, containerID, image, role string) {
	if _, err := api.ContainerRemove(
		context.WithoutCancel(ctx), containerID, client.ContainerRemoveOptions{Force: true},
	); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("container", containerID).Str("image", image).
			Msgf("remove %s container", role)
	}
}

// attachOneShotContainer attaches to the stdio spec exchanges with containerID.
func attachOneShotContainer(
	ctx context.Context, api dockerAPI, containerID string, spec model.OneShotSpec,
) (client.HijackedResponse, error) {
	attached, err := api.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  spec.Stdin != nil,
		Stdout: spec.Stdout != nil,
		Stderr: spec.Stderr != nil,
	})
	if err != nil {
		return client.HijackedResponse{}, fmt.Errorf("attach to %s container: %w", spec.Image, err)
	}
	return attached.HijackedResponse, nil
}

// serveExec starts spec.Exec and runs Exec.Serve on its stream in the background.
// The returned stop closes the stream and waits for Serve, logging its error.
func serveExec(ctx context.Context, api dockerAPI, containerID string, spec model.OneShotSpec) (func(), error) {
	stream, err := openExecStream(ctx, api, containerID, *spec.Exec)
	if err != nil {
		return nil, err
	}

	served := make(chan error, 1)
	go func() { served <- spec.Exec.Serve(ctx, stream) }()
	// Serve may not watch ctx; closing the stream ends it either way.
	stopOnCancel := context.AfterFunc(ctx, stream.release)

	return func() {
		stopOnCancel()
		stream.release()
		err := <-served
		if err == nil {
			return
		}
		level := zerolog.WarnLevel
		if ctx.Err() != nil {
			level = zerolog.DebugLevel
		}
		zerolog.Ctx(ctx).WithLevel(level).Err(err).Str("container", containerID).Str("image", spec.Image).
			Msg("serve one-shot container exec")
	}, nil
}

// isContainerRunning reports whether containerID still runs; a container the
// daemon cannot inspect counts as running, so the caller does not wait on it.
func isContainerRunning(ctx context.Context, api dockerAPI, containerID string) bool {
	inspect, err := api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil || inspect.Container.State == nil {
		return true
	}
	return inspect.Container.State.Running
}

// exchange pumps the caller's streams through the attached connection while the
// container runs, and returns once both directions are done.
//
// The writer and the reader are concurrent on purpose: filling stdin before
// reading any stdout deadlocks on any payload larger than the pipe buffer — the
// container blocks writing output nobody drains, arcave blocks writing input nobody
// reads, and neither ever gets to the other half.
func exchange(ctx context.Context, attached client.HijackedResponse, spec model.OneShotSpec) error {
	var eg errgroup.Group

	if spec.Stdin != nil {
		eg.Go(func() error {
			stdin := &recordingReader{r: spec.Stdin}
			// A container that exits without draining its stdin (think `head`) fails
			// this copy on the *write* side, which is not an error: the exit status
			// decides that run's outcome. A failure to read the caller's own source
			// is an error, and is the only one reported here.
			if _, err := io.Copy(attached.Conn, stdin); err != nil && stdin.err != nil {
				return fmt.Errorf("read %s container input: %w", spec.Image, stdin.err)
			}
			// Half-close so the process sees EOF on stdin rather than hanging.
			if err := attached.CloseWrite(); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Str("image", spec.Image).
					Msg("half-close one-shot container stdin")
			}
			return nil
		})
	}

	eg.Go(func() error {
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

	if err := eg.Wait(); err != nil {
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

// waitOneShotContainer waits for containerID to exit; a non-zero exit is a
// model.ContainerExitError carrying the container's output.
func waitOneShotContainer(ctx context.Context, api dockerAPI, containerID, image string) error {
	wait := api.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case err := <-wait.Error:
		return fmt.Errorf("wait for %s container: %w", image, err)
	case status := <-wait.Result:
		if status.StatusCode == 0 {
			return nil
		}
		output := containerOutput(ctx, api, containerID)
		zerolog.Ctx(ctx).Debug().
			Str("container", containerID).
			Str("image", image).
			Strs("container_output", output).
			Msg("one-shot container exited non-zero")
		return &model.ContainerExitError{
			Image:      image,
			StatusCode: status.StatusCode,
			Output:     output,
		}
	}
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
	rc, err := api.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
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

// volumeHolderCmd is what the volume holder runs: it exists to hold the volume,
// so it exits at once and is only ever copied in and out of.
var volumeHolderCmd = []string{"true"}

// withVolumeHolder runs fn against a volume holder with source mounted
// at volumeHolderMount, and removes it when fn returns.
func withVolumeHolder(
	ctx context.Context, api dockerAPI, src model.MountSource, fn func(id string) error,
) error {
	id, err := startVolumeHolder(ctx, api, src)
	if err != nil {
		return err
	}
	defer removeVolumeHolder(ctx, api, id)

	return fn(id)
}

// startVolumeHolder creates a volume holder — a container that only holds source
// at volumeHolderMount for the archive API to reach — runs it to exit, and
// returns its id. The caller removes it with removeVolumeHolder: a stream copied
// out of the container has to outlive the call that opened it.
//
// The run to exit is not ceremony: the daemon populates a volume when the
// container that mounts it starts, so copying before that would read a mount
// nothing has set up yet.
func startVolumeHolder(
	ctx context.Context, api dockerAPI, src model.MountSource,
) (string, error) {
	if src.Source == "" {
		return "", fmt.Errorf("cannot reach a %q volume with no source", src.Type)
	}
	if err := ensureImage(ctx, api, VolumeToolsImage); err != nil {
		return "", err
	}

	created, err := api.ContainerCreate(ctx,
		client.ContainerCreateOptions{
			Config: &container.Config{Image: VolumeToolsImage, Cmd: volumeHolderCmd, Labels: newOneShotLabels()},
			HostConfig: &container.HostConfig{
				Binds:       []string{src.Source + ":" + volumeHolderMount},
				SecurityOpt: VolumeSecurityOpt,
			},
		})
	if err != nil {
		return "", fmt.Errorf("create %s container: %w", VolumeToolsImage, err)
	}

	if _, err := api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		removeVolumeHolder(ctx, api, created.ID)
		return "", fmt.Errorf("start %s container: %w", VolumeToolsImage, err)
	}
	wait := api.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case err := <-wait.Error:
		removeVolumeHolder(ctx, api, created.ID)
		return "", fmt.Errorf("wait for %s container: %w", VolumeToolsImage, err)
	case <-wait.Result:
	}
	return created.ID, nil
}

// removeVolumeHolder force-removes a volume holder, logging a failure.
func removeVolumeHolder(ctx context.Context, api dockerAPI, containerID string) {
	removeOneShotContainer(ctx, api, containerID, VolumeToolsImage, "volume holder")
}
