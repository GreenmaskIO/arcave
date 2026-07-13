// Package docker acts on the *running* stack, in contrast to internal/client/
// compose, which only reads what the compose files declare. It holds the two
// types GM needs to reach the daemon, split by what they act on rather than by
// how they get there:
//
//   - Engine (docker.go) moves a volume's bytes in and out of a service
//     container, through the Docker SDK — no CLI output to parse, no helper image.
//   - ComposeRunner (compose.go) stops and starts the project's services,
//     through the docker CLI, since the Docker API has no Compose equivalent and
//     the CLI inherits the user's context, credentials and build settings.
//
// They share one unexported seam, the runner newRunner builds (options.go):
// Engine also shells out once, to wipe a mount with a throwaway container. That
// shared helper — and the Options both halves must honour, or they would target
// different daemons — is why the two live in one package.
package docker

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

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

// Engine is GM's volume I/O against the Docker daemon: it streams a service
// mount's contents out (OpenMount) and wipes-and-extracts a stream back in
// (WriteMount). These are the operations that have no Compose-level equivalent,
// so they go through the Docker API rather than the CLI — see ComposeRunner for
// the lifecycle half.
//
// One Engine is created per command at the DI root and injected into both
// snapshot backends; it is concurrency-safe, and its Close belongs to whoever
// created it.
type Engine struct {
	cli *client.Client
	// run executes a docker CLI invocation (used to wipe mounts via a throwaway
	// container); it is built by newRunner and is a field only so tests can
	// exercise wipeMount's argument assembly without a daemon.
	run func(ctx context.Context, args ...string) error
}

// NewEngine builds a Docker SDK client from the environment (DOCKER_HOST, the
// active context, TLS settings), overridden by whatever opts sets. It does not
// contact the daemon: API-version negotiation is deferred to the first call, so
// a command that never touches a mount (savepoint list) can construct an Engine
// with docker stopped.
//
// opts.Context is rejected without opts.Host. A docker *context* is a CLI
// concept — resolving one needs docker/cli's context store, which this package
// does not depend on — so the SDK client cannot honour it. Accepting it here
// would leave the CLI half of this package talking to the context's daemon while
// the SDK half talked to DOCKER_HOST's: a savepoint exported from one and
// restored into the other. Naming the host makes both halves agree.
func NewEngine(opts Options) (*Engine, error) {
	if opts.Context != "" && opts.Host == "" {
		return nil, fmt.Errorf("docker.context %q needs docker.host set too: the Docker API client cannot resolve a CLI context, so without a host the API and CLI halves of gm would target different daemons", opts.Context)
	}

	// FromEnv first, so anything opts leaves unset keeps its environment value.
	clientOpts := []client.Opt{client.FromEnv}
	if opts.Host != "" {
		clientOpts = append(clientOpts, client.WithHost(opts.Host))
	}
	if opts.CertPath != "" || opts.TLSVerify {
		clientOpts = append(clientOpts, client.WithTLSClientConfig(
			filepath.Join(opts.CertPath, "ca.pem"),
			filepath.Join(opts.CertPath, "cert.pem"),
			filepath.Join(opts.CertPath, "key.pem"),
		))
	}
	if opts.APIVersion != "" {
		clientOpts = append(clientOpts, client.WithVersion(opts.APIVersion))
	} else {
		clientOpts = append(clientOpts, client.WithAPIVersionNegotiation())
	}

	cli, err := client.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("connect to docker engine: %w", err)
	}
	return &Engine{
		cli: cli,
		run: newRunner(opts),
	}, nil
}

func (e *Engine) Close() error { return e.cli.Close() }

// OpenMount opens a tar stream of the service mount at target's contents.
//
// It locates the project's container for service and streams its target path
// out via the Docker API. Keying on service + container target (rather than a
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
// owned by the caller; Docker never touches the host filesystem.
func (e *Engine) WriteMount(ctx context.Context, projectName, service, target string, r io.Reader) error {
	id, mnt, err := e.findServiceMount(ctx, projectName, service, target)
	if err != nil {
		return fmt.Errorf("finding service mounts: %w", err)
	}

	if err := wipeMount(ctx, mnt, e.run); err != nil {
		return fmt.Errorf("clear %s mount %s before restore: %w", service, target, err)
	}

	// readOnly hides r's Close from the SDK: CopyToContainer passes the stream to
	// net/http as the request body, and http.NewRequestWithContext type-asserts it
	// to io.ReadCloser — so the transport would close the caller's stream, and the
	// caller's own deferred Close would then fail with "file already closed".
	if err := e.cli.CopyToContainer(ctx, id, filepath.Dir(target), readOnly{r}, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("restore %s mount %s: %w", service, target, err)
	}
	return nil
}

// readOnly wraps a reader so that no io.Reader method set other than Read is
// visible, even when the underlying value also implements io.Closer.
type readOnly struct{ r io.Reader }

func (ro readOnly) Read(p []byte) (int, error) { return ro.r.Read(p) }

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
