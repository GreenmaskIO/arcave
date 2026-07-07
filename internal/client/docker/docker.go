// Package docker controls Docker for GM. Compose-level operations (stop/up) are
// run through the docker CLI, since the Engine API has no Compose equivalent and
// the CLI inherits the user's context, credentials and build settings. Container
// and volume operations use the Docker Engine SDK directly, which avoids parsing
// CLI output and depending on a helper image.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// composeProjectLabel and composeServiceLabel are set by Docker Compose on
// every container it creates.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)

// ComposeClient runs docker compose commands against a fixed set of compose
// files, mirroring how the user would invoke docker compose by hand.
type ComposeClient struct {
	files []string
	// run executes a docker CLI invocation; it defaults to runDocker and is a
	// field only so tests can capture the assembled arguments without a daemon.
	run func(ctx context.Context, args ...string) error
}

func NewComposeClient(files ...string) *ComposeClient {
	return &ComposeClient{files: files, run: runDocker}
}

// Stop stops all project services. docker compose stop only returns once the
// containers have exited, so no extra polling is needed.
func (c *ComposeClient) Stop(ctx context.Context) error {
	return c.run(ctx, c.composeArgs("stop")...)
}

// DefaultUpWaitTimeout is the default bound (seconds) for Up's health-check wait.
const DefaultUpWaitTimeout = 120

// Up starts all project services in the background and waits until they are
// healthy, honouring container health checks where defined (--wait).
//
// waitTimeoutSec bounds how long it blocks on health checks: without a bound, a
// service that never becomes healthy (e.g. a crash-looping container with
// restart: always) would make Up hang forever, so the bound turns that into an
// error callers can act on (e.g. rollback reverts to its safety backup). A
// value <= 0 falls back to DefaultUpWaitTimeout.
func (c *ComposeClient) Up(ctx context.Context, waitTimeoutSec int) error {
	if waitTimeoutSec <= 0 {
		waitTimeoutSec = DefaultUpWaitTimeout
	}
	timeout := strconv.Itoa(waitTimeoutSec)
	return c.run(ctx, c.composeArgs("up", "-d", "--wait", "--wait-timeout", timeout)...)
}

func (c *ComposeClient) composeArgs(args ...string) []string {
	out := []string{"compose"}
	for _, f := range c.files {
		out = append(out, "-f", f)
	}
	return append(out, args...)
}

func runDocker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

// Engine talks to the Docker daemon through the Engine API for operations that
// have no Compose-level equivalent — currently copying volume data out of
// service containers.
type Engine struct {
	cli *client.Client
	// run executes a docker CLI invocation (used to wipe mounts via a throwaway
	// container); it defaults to runDocker and is a field only so tests can
	// exercise wipeMount's argument assembly without a daemon.
	run func(ctx context.Context, args ...string) error
}

// NewEngine connects to the Docker daemon using the standard environment
// (DOCKER_HOST, the active context, TLS settings) and negotiates the API
// version with the server.
func NewEngine() (*Engine, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect to docker engine: %w", err)
	}
	return &Engine{cli: cli, run: runDocker}, nil
}

func (e *Engine) Close() error { return e.cli.Close() }

// OpenMount opens a tar stream of the service mount at target's contents.
//
// It locates the project's container for service and streams its target path
// out via the Engine API. Keying on service + container target (rather than a
// volume name) lets the same code export named volumes, bind mounts, and
// anonymous volumes uniformly. GM snapshots while services are stopped but not
// removed, so the service container itself is used — no helper image is
// required for export. The archive is streamed verbatim rather than extracted so
// that file ownership and permissions (which an unprivileged host process could
// not reapply) survive a later restore. The caller owns the returned stream and
// closes it; Docker stays out of the host filesystem entirely.
func (e *Engine) OpenMount(ctx context.Context, projectName, service, target string) (io.ReadCloser, error) {
	containerID, _, err := e.findServiceMount(ctx, projectName, service, target)
	if err != nil {
		return nil, err
	}

	rc, _, err := e.cli.CopyFromContainer(ctx, containerID, target)
	if err != nil {
		return nil, fmt.Errorf("export %s mount %s: %w", service, target, err)
	}
	return rc, nil
}

// mountInfo carries the backing-storage details of a resolved container mount,
// used to clear it before a restore.
type mountInfo struct {
	Type   string // "volume" or "bind"
	Name   string // docker volume name (volume mounts)
	Source string // host path (bind mounts)
}

// findServiceMount returns the ID of the project's container for service that
// mounts target, along with that mount's backing-storage details. Stopped
// containers are included, since export and restore run while services are down.
func (e *Engine) findServiceMount(ctx context.Context, projectName, service, target string) (id string, mnt mountInfo, err error) {
	f := filters.NewArgs(
		filters.Arg("label", composeProjectLabel+"="+projectName),
		filters.Arg("label", composeServiceLabel+"="+service),
	)
	containers, err := e.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return "", mountInfo{}, fmt.Errorf("list containers for service %q: %w", service, err)
	}
	id, mnt, ok := matchMount(containers, target)
	if !ok {
		return "", mountInfo{}, fmt.Errorf("no container for service %q in project %q mounts %q (is the project created?)", service, projectName, target)
	}
	return id, mnt, nil
}

// matchMount finds, among containers, the one that mounts target and returns its
// ID and that mount's backing-storage details. This is the pure decision split
// out of findServiceMount so it can be tested without a daemon; ok is false when
// no container mounts target.
func matchMount(containers []container.Summary, target string) (id string, mnt mountInfo, ok bool) {
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Destination == target {
				return c.ID, mountInfo{Type: string(m.Type), Name: m.Name, Source: m.Source}, true
			}
		}
	}
	return "", mountInfo{}, false
}

// WriteMount restores the service mount at target from the tar stream r.
//
// It clears the mount's current contents (via wipeMount) and then extracts the
// stream back into the container, the mirror of OpenMount. Because the archive's
// top entry is the mount basename, it is extracted into the parent of target so
// the data lands back exactly at target. Like export, this runs while services
// are stopped but not removed, so the service container is reused. The stream is
// owned by the caller (volume.Copy closes its source); Docker never touches the
// host filesystem.
func (e *Engine) WriteMount(ctx context.Context, projectName, service, target string, r io.Reader) error {
	id, mnt, err := e.findServiceMount(ctx, projectName, service, target)
	if err != nil {
		return fmt.Errorf("finding service mounts: %w", err)
	}

	if err := wipeMount(ctx, mnt, e.run); err != nil {
		return fmt.Errorf("clear %s mount %s before restore: %w", service, target, err)
	}

	if err := e.cli.CopyToContainer(ctx, id, filepath.Dir(target), r, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("restore %s mount %s: %w", service, target, err)
	}
	return nil
}

// wipeMount removes every entry of a mount's backing storage by running a
// throwaway root container that mounts it. This is the only host-agnostic way to
// delete data the service wrote as root: the daemon owns those files, and
// CopyToContainer only merges entries, never deletes them. Volumes (named and
// anonymous) are mounted by volume name; bind mounts by host path. run executes
// the docker CLI (Engine.run in production, a stub in tests).
func wipeMount(ctx context.Context, mnt mountInfo, run func(ctx context.Context, args ...string) error) error {
	var src string
	switch mnt.Type {
	case "volume":
		src = mnt.Name
	case "bind":
		src = mnt.Source
	default:
		return fmt.Errorf("unsupported mount type %q", mnt.Type)
	}
	if src == "" {
		return fmt.Errorf("cannot resolve backing storage for %q mount", mnt.Type)
	}
	// find -mindepth 1 clears hidden entries too; -exec ... + batches the removal.
	return run(ctx, "run", "--rm", "-v", src+":/data", "busybox",
		"find", "/data", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "{}", "+")
}
