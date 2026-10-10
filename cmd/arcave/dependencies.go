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

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/api"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/tlsconfig"
	"github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/storages"
	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	cfgrepo "github.com/greenmaskio/arcave/internal/repository/config"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/service/printer/json"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// resticShutdownTimeout bounds the restic server's graceful shutdown.
const resticShutdownTimeout = 5 * time.Second

// dependencies is the composition root's bag of per-command collaborators.
type dependencies struct {
	composeProjectStore storages.Storager
	progressWriter      io.WriteCloser
	dockerCli           *command.DockerCli
	dockerClient        client.APIClient
	composeService      api.Compose
	engine              *docker.Engine
	composeRunner       *compose.Client
	// resticServer is held for one reason only — so Close shuts it down. What goes
	// down to the backend is the URL base, never the object.
	resticServer *echo.Echo
	service      *core.Service
	printer      *json.Printer
}

// initAllDependencies wires the savepoint workflows for the project rooted at
// projectPath. Every store arrives from storefactory already rooted.
func initAllDependencies(
	ctx context.Context, projectPath string, cfg config.Config, composeProject model.DockerComposeProjectName,
) (*dependencies, error) {
	// One unwind for every failure path: whatever is open is released newest first.
	var opened partial
	defer func() { opened.releaseOnFailure(ctx) }()

	// One store for the whole compose project: savepoint records, fullcopy captures and a
	// cdc repository root alike, rooted inside the session that they die with.
	composeProjectStore, err := storefactory.New(
		cfg.Storage, projectPath,
		model.ArcaveProjectName(cfg.Project.Name), model.DefaultSession, composeProject,
	)
	if err != nil {
		return nil, fmt.Errorf("initialise compose project store: %w", err)
	}
	opened.add("compose project store", composeProjectStore)

	// Where compose's own progress goes, on the logger initRoot resolved.
	progressWriter := composeProgressWriter(cfg.Compose.Progress, *log.Ctx(ctx))
	opened.add("compose progress writer", progressWriter)

	// One docker/cli instance is the single daemon connection both halves of
	// client/docker are built over, so they cannot target different daemons. Its
	// closers belong to the returned dependencies — never deferred here.
	dockerCli, dockerClientAPI, err := initDockerClient(cfg.Docker, progressWriter)
	if err != nil {
		return nil, fmt.Errorf("initialise docker client: %w", err)
	}
	engine := docker.NewEngine(dockerClientAPI)
	opened.add("docker engine", engine)

	// Over the same daemon connection the engine uses.
	inspector := introspect.NewInspector(dockerClientAPI, composeProject)
	assembler := composespec.NewAssembler()
	codec := composespec.NewCodec()

	composeService := dockercompose.NewComposeService(dockerCli)
	composeRunner := compose.NewClient(composeService, compose.Options{
		WaitTimeout: cfg.Compose.WaitTimeout,
		StopTimeout: cfg.Compose.StopTimeout,
	})

	// The effective cdc section, defaults included even where the project carries
	// no block: a rollback restores through the backend its record names, so cdc
	// is wired whatever snapshot.backend says.
	cdcCfg := config.CDCWithDefaults(cfg.Backends.CDC)

	// The tree every cdc repository lives in — one directory per volume, named by
	// its ObjectID, beside the compose project's savepoint directories. Resolved once and
	// handed to both sides, so "the server serves exactly the tree cdc writes
	// into" is visible in one place rather than kept by two call sites agreeing.
	resticRoot, err := composeProjectStore.SubStorage(cdc.RepoNamespace, true)
	if err != nil {
		return nil, fmt.Errorf("initialise restic repository root: %w", err)
	}

	// The server cdc's containers fetch their repositories over. It listens for
	// the whole process, so an address that will not bind fails the command up
	// front, and the port it took is known here — before the first backend.
	resticServer := rest.NewEcho(ctx, repo.NewResolver(resticRoot))
	resticPort, err := rest.Serve(resticServer, cdcCfg.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("start the restic server: %w", err)
	}
	opened.add("restic server", resticServer)

	cdcHostNetwork, cdcContainerHost, err := resolveCDCTransport(ctx, cdcCfg, engine)
	if err != nil {
		return nil, err
	}

	// The one place a volume's Backend becomes an instance, and the only place
	// that names the engine — through the seam its backend declares.
	backendRegistry, err := core.NewSnapshotBackendRegistry(
		fullcopy.NewFactory(engine),
		cdc.NewFactory(cdc.Dependencies{
			Runner: engine,
			// The container's own name for the server, joined to the port it bound:
			// the value the backend is handed, so nothing below holds the server.
			RepoURLBase:    rest.RepositoryURLBase(cdcCfg.ServerScheme, cdcContainerHost, resticPort),
			RepositoryRoot: resticRoot,
			Options:        cdcOptions(cdcCfg, cdcHostNetwork),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build snapshot backend registry: %w", err)
	}

	imageBackend, err := newImageBackend(composeProjectStore, engine)
	if err != nil {
		return nil, err
	}

	// One repository over the one store, so every record travels with its data. The
	// locker is bound to the project root here, so the service never names a path.
	svc := core.New(core.Deps{
		SavepointRepo:    sprepo.New(composeProjectStore),
		ProjectLocker:    lock.New(projectPath),
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		ComposeRunner:    composeRunner,
		VolumeRemover:    engine,
		ImageBackend:     imageBackend,
		MinDaemonMajor:   dedupcopy.MinDaemonMajor,
		// The per-replica half of a layer restore: what compose's one image per
		// service cannot express, the engine rebuilds container by container.
		ContainerRebuilder: engine,
		BackendRegistry:    backendRegistry,
		// The marker an interrupted rollback leaves behind, host-local beside the lock.
		Inflight:       inflightrepo.New(projectPath),
		Store:          composeProjectStore,
		DefaultBackend: cfg.Snapshot.BackendKind(),
	})
	opened.keep()
	return &dependencies{
		composeProjectStore: composeProjectStore,
		progressWriter:      progressWriter,
		dockerCli:           dockerCli,
		dockerClient:        dockerClientAPI,
		composeService:      composeService,
		engine:              engine,
		composeRunner:       composeRunner,
		resticServer:        resticServer,
		service:             svc,
		printer:             json.New(os.Stdout),
	}, nil
}

// resolveCDCTransport turns backends.cdc.network_mode into the transport the
// restic containers use: whether they join the host's network namespace, and
// the host their repository URL names. Auto probes the daemon, but mispicks
// host networking for a remote Linux daemon — bridge is the escape hatch there.
func resolveCDCTransport(
	ctx context.Context, cfg config.CDCBackend, engine *docker.Engine,
) (hostNetwork bool, containerHost string, err error) {
	switch cfg.NetworkMode {
	case config.NetworkModeHost:
		return true, "127.0.0.1", nil
	case config.NetworkModeBridge:
		return false, cfg.ContainerHost, nil
	default: // config.NetworkModeAuto — Validate admits nothing else.
		shared, err := engine.HostLoopbackShared(ctx)
		if err != nil {
			return false, "", fmt.Errorf("resolve backends.cdc.network_mode %q: %w", cfg.NetworkMode, err)
		}
		if shared {
			return true, "127.0.0.1", nil
		}
		return false, cfg.ContainerHost, nil
	}
}

// cdcOptions translates the backends.cdc section into the backend's own
// options, plus the transport resolveCDCTransport picked.
func cdcOptions(cfg config.CDCBackend, hostNetwork bool) cdc.Options {
	return cdc.Options{
		Image:       cfg.Image,
		Incremental: cfg.RestoreMode == config.RestoreModeIncremental,
		Overwrite:   cfg.Overwrite,
		HostNetwork: hostNetwork,
	}
}

// partial tracks what a half-finished wiring has opened. releaseOnFailure is
// deferred once and does nothing after keep, which the success path calls.
type partial struct {
	closers []namedCloser
	kept    bool
}

type namedCloser struct {
	name string
	c    io.Closer
}

func (p *partial) add(name string, c io.Closer) { p.closers = append(p.closers, namedCloser{name, c}) }

func (p *partial) keep() { p.kept = true }

func (p *partial) releaseOnFailure(ctx context.Context) {
	if p.kept {
		return
	}
	for i := len(p.closers) - 1; i >= 0; i-- {
		closeQuietly(ctx, p.closers[i].name, p.closers[i].c)
	}
}

// Close releases the restic server, the Docker engine, the progress writer and
// the store this root owns; only the store's error is returned. The progress
// writer is closed after the engine, so a line compose left unterminated is
// still emitted. dockerClient is not closed — the engine was built over it.
func (d *dependencies) Close(ctx context.Context) error {
	shutdownResticServer(ctx, d.resticServer)
	closeQuietly(ctx, "docker engine", d.engine)
	closeQuietly(ctx, "compose progress writer", d.progressWriter)
	return d.composeProjectStore.Close()
}

// shutdownResticServer stops serving gracefully: restic in a container reports a
// severed connection as a repository error rather than as the shutdown it was.
// The deadline is detached from ctx, which is routinely already cancelled by the
// time Close runs from a defer — and a cancelled Shutdown severs at once.
func shutdownResticServer(ctx context.Context, e *echo.Echo) {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resticShutdownTimeout)
	defer cancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("shut down restic server")
	}
}

// initServiceDeps wires `arcave init`: somewhere to write the config, nothing
// more. Nothing here holds a resource.
func initServiceDeps(projectPath string) (*core.InitService, *json.Printer) {
	return core.NewInitService(projectPath, cfgrepo.New(projectPath)), json.New(os.Stdout)
}

// closeQuietly closes c and logs any error without failing the caller.
func closeQuietly(ctx context.Context, name string, c io.Closer) {
	if err := c.Close(); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("close " + name)
	}
}

func getDockerOptions(cfg config.Docker) (*flags.ClientOptions, error) {
	if cfg.Host != "" && cfg.Context != "" {
		return nil, fmt.Errorf("docker.host and docker.context are mutually exclusive: %q already names a daemon, so remove one of the two", cfg.Context)
	}

	opts := flags.NewClientOptions()
	if cfg.Host != "" {
		opts.Hosts = []string{cfg.Host}
	}
	opts.Context = cfg.Context

	tlsOpts, useTLS, err := dockerTLSOptions(cfg)
	if err != nil {
		return nil, err
	}
	if useTLS {
		opts.TLS = true
		opts.TLSVerify = cfg.TLSVerify
		opts.TLSOptions = tlsOpts
	}
	return opts, nil
}

// dockerTLSOptions resolves the docker.* TLS fields into docker/cli's TLSOptions
// and reports whether any TLS material was configured (all-empty means "read the
// environment"). cert_file/key_file are a mutual-TLS pair, checked here because
// docker/cli would surface a half-set one only as an opaque handshake failure.
func dockerTLSOptions(cfg config.Docker) (*tlsconfig.Options, bool, error) {
	caFile := tlsFile(cfg.CAFile, cfg.CertPath, "ca.pem")
	certFile := tlsFile(cfg.CertFile, cfg.CertPath, "cert.pem")
	keyFile := tlsFile(cfg.KeyFile, cfg.CertPath, "key.pem")

	switch {
	case certFile != "" && keyFile == "":
		return nil, false, fmt.Errorf("docker.cert_file is set but docker.key_file is missing: a client certificate needs both, or set docker.cert_path to supply the pair from one directory")
	case keyFile != "" && certFile == "":
		return nil, false, fmt.Errorf("docker.key_file is set but docker.cert_file is missing: a client certificate needs both, or set docker.cert_path to supply the pair from one directory")
	}

	// keyFile is not tested: the switch above leaves the pair either both set or both empty.
	if caFile == "" && certFile == "" && !cfg.TLSVerify {
		return nil, false, nil
	}

	return &tlsconfig.Options{
		CAFile:   caFile,
		CertFile: certFile,
		KeyFile:  keyFile,
	}, true, nil
}

// tlsFile resolves one TLS file path the way the docker CLI does: an explicit
// path wins, then CertPath/name (DOCKER_CERT_PATH); neither set leaves it empty,
// so docker/cli reads the environment rather than a bogus path.
func tlsFile(explicit, certPath, name string) string {
	if explicit != "" {
		return explicit
	}
	if certPath != "" {
		return filepath.Join(certPath, name)
	}
	return ""
}

// nopWriteCloser makes a stream arcave does not own — os.Stderr, io.Discard —
// safe to hand to a Close that closes whatever writer it was given. Spelled out
// because the stdlib has no writer counterpart to io.NopCloser (golang/go#22823).
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// composeProgressWriter translates compose.progress into the library's global
// render mode (progress.Mode) plus the writer compose renders on. The destination
// is always stderr or nothing — stdout carries arcave's JSON — and the default
// re-emits compose's events as arcave log records, so the run speaks one format.
func composeProgressWriter(mode string, logger zerolog.Logger) io.WriteCloser {
	switch mode {
	case config.ProgressQuiet:
		// Discarding the stream too covers the warnings compose writes outside the UI.
		progress.Mode = progress.ModeQuiet
		return nopWriteCloser{io.Discard}
	case config.ProgressTTY:
		// ModeAuto, not ModeTTY: compose falls back to plain lines in a pipe.
		progress.Mode = progress.ModeAuto
		return nopWriteCloser{os.Stderr}
	default:
		progress.Mode = progress.ModeJSON
		return compose.NewProgressLogger(logger)
	}
}

// initDockerClient resolves cfg into the one docker/cli instance both halves of
// client/docker are built over, plus the APIClient it uses. progressOut becomes
// the CLI's error stream once the endpoint resolves; until then that stream is a
// buffer, being the only place docker/cli reports a failed resolution.
func initDockerClient(cfg config.Docker, progressOut io.Writer) (*command.DockerCli, client.APIClient, error) {
	clientOpts, err := getDockerOptions(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// DOCKER_API_VERSION is the only seam docker/cli leaves for pinning the API
	// version. Safe to set in-process: arcave spawns no children, so this configures
	// arcave's own client and nothing else.
	if cfg.APIVersion != "" {
		if err := os.Setenv(client.EnvOverrideAPIVersion, cfg.APIVersion); err != nil {
			return nil, nil, fmt.Errorf("pin docker api version %q: %w", cfg.APIVersion, err)
		}
	}

	// stdout is discarded — arcave's own output contract is JSON there. stderr is a
	// buffer for now: it is the only channel docker/cli reports a failed endpoint
	// resolution on, and the buffer turns that report into an error below.
	var initErr bytes.Buffer
	dockerCli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(&initErr),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}
	if err := dockerCli.Initialize(clientOpts); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// Built here rather than left to dockerCli.Client(), which answers a failure
	// with os.Exit(1). NewAPIClientFromFlags runs the same resolution and returns an
	// error; injecting it back keeps the CLI and compose on this very client.
	apiClient, err := command.NewAPIClientFromFlags(clientOpts, dockerCli.ConfigFile())
	if err != nil {
		return nil, nil, fmt.Errorf("connect to docker engine (docker.host %q, docker.context %q): %w", cfg.Host, cfg.Context, err)
	}
	if err := command.WithAPIClient(apiClient)(dockerCli); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// Initialize can log a benign warning of its own (a malformed
	// ~/.docker/config.json), so from here on the buffer holds only the resolution.
	initErr.Reset()

	// Force the CLI's own endpoint resolution now, while its failure is still
	// arcave's to report: DockerEndpoint and Client() run the same sync.Once, but
	// Client() prints the cause and exits, and compose calls it itself.
	dockerCli.DockerEndpoint()
	if initErr.Len() > 0 {
		return nil, nil, fmt.Errorf("connect to docker engine (docker.host %q, docker.context %q): %s",
			cfg.Host, cfg.Context, strings.TrimSpace(initErr.String()))
	}

	// Resolution succeeded, so the error stream becomes compose's progress channel.
	if err := command.WithErrorStream(progressOut)(dockerCli); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}
	return dockerCli, apiClient, nil
}

// composeProjectBlobPrefix sits beside the compose project's savepoints, shared by them and
// nothing wider; a savepoint id is a canonical UUID, so the name cannot collide with one.
const composeProjectBlobPrefix = "shared/oci/blobs"

// newImageBackend builds the one image backend: layers are always deduplicated,
// which needs a daemon of Docker 25 or newer.
func newImageBackend(composeProjectStore storages.Storager, engine *docker.Engine) (*imagecopy.Copier, error) {
	blobStore, err := composeProjectStore.SubStorage(composeProjectBlobPrefix, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at the shared blob store: %w", err)
	}
	return imagecopy.NewCopier(composeProjectStore, engine, dedupcopy.New(blobStore)), nil
}
