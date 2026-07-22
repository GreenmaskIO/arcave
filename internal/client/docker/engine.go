// Package docker acts on the *running* stack, in contrast to internal/client/
// compose, which only reads what the compose files declare. It holds the two
// types GM needs to reach the daemon, split by what they act on:
//
//   - Engine (engine.go) moves a volume's bytes in and out of a service
//     container, through the Docker API.
//   - ComposeRunner (loader.go) stops and starts the project's services,
//     through the compose library (github.com/docker/compose/v2), since the
//     Docker API itself has no Compose equivalent.
//
// Both reach the daemon through Go libraries — GM shells out to nothing, so it
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
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// composeProjectLabel and composeServiceLabel are set by Docker Compose on
// every container it creates.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)

// wipeImage is the throwaway image wipeMount runs to clear a mount: small,
// ubiquitous, and carrying the `find` GM needs.
const wipeImage = "busybox"

// dockerAPI is the slice of the Docker API client this package actually calls —
// consumer-side, so a test can drive Engine with a mock instead of a daemon.
// *client.Client (and so docker/cli's client.APIClient) satisfies it.
type dockerAPI interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerAttach(ctx context.Context, containerID string, options container.AttachOptions) (types.HijackedResponse, error)
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error)
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options container.CopyToContainerOptions) error
	ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	Close() error
}

// Engine is GM's volume I/O against the Docker daemon: it streams a service
// mount's contents out (OpenMount) and wipes-and-extracts a stream back in
// (WriteMount). These are the operations that have no Compose-level equivalent,
// so they go through the Docker API — see ComposeRunner for the lifecycle half.
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
	containerID, _, err := findServiceMount(ctx, e.api, projectName, service, target)
	if err != nil {
		return nil, err
	}

	rc, _, err := e.api.CopyFromContainer(ctx, containerID, target)
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
func findServiceMount(ctx context.Context, api dockerAPI, projectName, service, target string) (id string, mnt mountInfo, err error) {
	f := filters.NewArgs(
		filters.Arg("label", composeProjectLabel+"="+projectName),
		filters.Arg("label", composeServiceLabel+"="+service),
	)
	containers, err := api.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
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
	id, mnt, err := findServiceMount(ctx, e.api, projectName, service, target)
	if err != nil {
		return fmt.Errorf("finding service mounts: %w", err)
	}

	if err := wipeMount(ctx, e.api, mnt); err != nil {
		return fmt.Errorf("clear %s mount %s before restore: %w", service, target, err)
	}

	// readOnly hides r's Close from the SDK: CopyToContainer passes the stream to
	// net/http as the request body, and http.NewRequestWithContext type-asserts it
	// to io.ReadCloser — so the transport would close the caller's stream, and the
	// caller's own deferred Close would then fail with "file already closed".
	if err := e.api.CopyToContainer(ctx, id, filepath.Dir(target), readOnly{r}, container.CopyToContainerOptions{}); err != nil {
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
// anonymous) are mounted by volume name; bind mounts by host path.
//
// It is the wipe *policy* — which image, which command, which bind — over the
// reusable one-shot container mechanism in oneshot.go, and exchanges no stdio.
func wipeMount(ctx context.Context, api dockerAPI, mnt mountInfo) error {
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

	return runOneShotContainer(ctx, api, oneShotSpec{
		image: wipeImage,
		// find -mindepth 1 clears hidden entries too; -exec ... + batches the removal.
		cmd:   []string{"find", "/data", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "{}", "+"},
		binds: []string{src + ":/data"},
	})
}
