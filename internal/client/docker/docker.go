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
	"os"
	"os/exec"
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
}

func NewComposeClient(files ...string) *ComposeClient {
	return &ComposeClient{files: files}
}

// Stop stops all project services. docker compose stop only returns once the
// containers have exited, so no extra polling is needed.
func (c *ComposeClient) Stop(ctx context.Context) error {
	return runDocker(ctx, c.composeArgs("stop")...)
}

// Up starts all project services in the background and waits until they are
// running, honouring container health checks where defined (--wait).
func (c *ComposeClient) Up(ctx context.Context) error {
	return runDocker(ctx, c.composeArgs("up", "-d", "--wait")...)
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
}

// NewEngine connects to the Docker daemon using the standard environment
// (DOCKER_HOST, the active context, TLS settings) and negotiates the API
// version with the server.
func NewEngine() (*Engine, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect to docker engine: %w", err)
	}
	return &Engine{cli: cli}, nil
}

func (e *Engine) Close() error { return e.cli.Close() }

// ExportMount writes the contents of the service mount at target to destTar as
// a tar archive.
//
// It locates the project's container for service and streams its target path
// out via the Engine API. Keying on service + container target (rather than a
// volume name) lets the same code export named volumes, bind mounts, and
// anonymous volumes uniformly. GM snapshots while services are stopped but not
// removed, so the service container itself is used — no helper image is
// required for export. The archive is stored verbatim rather than extracted so
// that file ownership and permissions (which an unprivileged host process could
// not reapply) survive a later restore.
func (e *Engine) ExportMount(ctx context.Context, projectName, service, target, destTar string) error {
	containerID, err := e.findServiceMount(ctx, projectName, service, target)
	if err != nil {
		return err
	}

	rc, _, err := e.cli.CopyFromContainer(ctx, containerID, target)
	if err != nil {
		return fmt.Errorf("export %s mount %s: %w", service, target, err)
	}
	defer rc.Close()

	f, err := os.Create(destTar)
	if err != nil {
		return fmt.Errorf("create snapshot archive: %w", err)
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return fmt.Errorf("write snapshot archive for %s %s: %w", service, target, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("flush snapshot archive for %s %s: %w", service, target, err)
	}
	return nil
}

// findServiceMount returns the ID of the project's container for service that
// mounts target. Stopped containers are included, since export runs while
// services are down.
func (e *Engine) findServiceMount(ctx context.Context, projectName, service, target string) (id string, err error) {
	f := filters.NewArgs(
		filters.Arg("label", composeProjectLabel+"="+projectName),
		filters.Arg("label", composeServiceLabel+"="+service),
	)
	containers, err := e.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return "", fmt.Errorf("list containers for service %q: %w", service, err)
	}
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Destination == target {
				return c.ID, nil
			}
		}
	}
	return "", fmt.Errorf("no container for service %q in project %q mounts %q (is the project created?)", service, projectName, target)
}
