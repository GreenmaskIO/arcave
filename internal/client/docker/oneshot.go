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
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/arcave/internal/model"
)

// oneShotSpec describes a throwaway container run to completion: which image,
// what command, what it mounts, and — optionally — the stdio streams the caller
// exchanges with it.
//
// The stdio fields are the extension point: a new kind of one-shot run adds a
// field rather than a parameter, so existing callers that only wipe a mount are
// untouched.
type oneShotSpec struct {
	image string
	cmd   []string
	binds []string

	// stdin, when set, is streamed to the container's stdin, which is then
	// half-closed so the process sees EOF. stdout and stderr, when set, receive
	// the demultiplexed container output. All three nil means no attach at all.
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// attaches reports whether the run exchanges any stdio with the container.
func (s oneShotSpec) attaches() bool {
	return s.stdin != nil || s.stdout != nil || s.stderr != nil
}

// runOneShotContainer drives one throwaway container through its whole
// lifecycle: ensure the image is present, create, attach (when the spec carries
// stdio), start, exchange, wait for exit, and always remove it. A non-zero exit
// returns an error carrying the container's own output, the way the CLI's
// combined output used to.
//
// This is the mechanism only — which image to run and what to mount it with is
// the caller's policy; see wipeMount for the one caller today.
func runOneShotContainer(ctx context.Context, api dockerAPI, spec oneShotSpec) error {
	if err := ensureImage(ctx, api, spec.image); err != nil {
		return err
	}

	cfg := &container.Config{
		Image:        spec.image,
		Cmd:          spec.cmd,
		OpenStdin:    spec.stdin != nil,
		StdinOnce:    spec.stdin != nil,
		AttachStdin:  spec.stdin != nil,
		AttachStdout: spec.stdout != nil,
		AttachStderr: spec.stderr != nil,
		// Tty stays false even with stdio: the framed stream is what lets stdout
		// and stderr be told apart on the way back.
	}
	created, err := api.ContainerCreate(ctx, cfg, &container.HostConfig{Binds: spec.binds}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create %s container: %w", spec.image, err)
	}
	defer func() {
		if err := api.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true}); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("container", created.ID).Str("image", spec.image).
				Msg("remove one-shot container")
		}
	}()

	// The attach happens before the start, not after: a container started first
	// can produce — and lose — output before anyone is listening.
	var attached types.HijackedResponse
	if spec.attaches() {
		attached, err = api.ContainerAttach(ctx, created.ID, container.AttachOptions{
			Stream: true,
			Stdin:  spec.stdin != nil,
			Stdout: spec.stdout != nil,
			Stderr: spec.stderr != nil,
		})
		if err != nil {
			return fmt.Errorf("attach to %s container: %w", spec.image, err)
		}
		defer attached.Close()
	}

	if err := api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start %s container: %w", spec.image, err)
	}

	if spec.attaches() {
		if err := exchange(ctx, attached, spec); err != nil {
			return err
		}
	}

	statusCh, errCh := api.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait for %s container: %w", spec.image, err)
	case status := <-statusCh:
		if status.StatusCode != 0 {
			output := containerOutput(ctx, api, created.ID)
			zerolog.Ctx(ctx).Debug().
				Str("container", created.ID).
				Str("image", spec.image).
				Strs("container_output", output).
				Msg("one-shot container exited non-zero")
			return &model.ContainerExitError{
				Image:      spec.image,
				StatusCode: status.StatusCode,
				Output:     output,
			}
		}
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
func exchange(ctx context.Context, attached types.HijackedResponse, spec oneShotSpec) error {
	var g errgroup.Group

	if spec.stdin != nil {
		g.Go(func() error {
			src := &recordingReader{r: spec.stdin}
			// A container that exits without draining its stdin (think `head`) fails
			// this copy on the *write* side, which is not an error: the exit status
			// decides that run's outcome. A failure to read the caller's own source
			// is an error, and is the only one reported here.
			if _, err := io.Copy(attached.Conn, src); err != nil && src.err != nil {
				return fmt.Errorf("read %s container input: %w", spec.image, src.err)
			}
			// Half-close so the process sees EOF on stdin rather than hanging.
			if err := attached.CloseWrite(); err != nil {
				zerolog.Ctx(ctx).Debug().Err(err).Str("image", spec.image).
					Msg("half-close one-shot container stdin")
			}
			return nil
		})
	}

	g.Go(func() error {
		stdout, stderr := spec.stdout, spec.stderr
		if stdout == nil {
			stdout = io.Discard
		}
		if stderr == nil {
			stderr = io.Discard
		}
		if _, err := stdcopy.StdCopy(stdout, stderr, attached.Reader); err != nil {
			return fmt.Errorf("read %s container output: %w", spec.image, err)
		}
		return nil
	})

	return g.Wait()
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
