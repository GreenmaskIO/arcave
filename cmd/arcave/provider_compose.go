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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/cli/cli/command"
	dockercontext "github.com/docker/cli/cli/context/docker"
	"github.com/docker/cli/cli/flags"
	dockeropts "github.com/docker/cli/opts"
	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	dockercompose "github.com/docker/compose/v5/pkg/compose"
	"github.com/docker/go-connections/tlsconfig"
	"github.com/greenmaskio/storages"
	"github.com/labstack/echo/v4"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/client/dockerdesktop"
	"github.com/greenmaskio/arcave/internal/client/podman"
	"github.com/greenmaskio/arcave/internal/client/podman/libpod"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/imagebackend"
	"github.com/greenmaskio/arcave/internal/service/imagebackend/dedupcopy"
	imagefullcopy "github.com/greenmaskio/arcave/internal/service/imagebackend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/status"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc/rest"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc/rest/repo"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/fullcopy"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// resticShutdownTimeout bounds the restic server's graceful shutdown.
const resticShutdownTimeout = 5 * time.Second

// composeResourceObject is the resource kind of a compose event whose id names no kind of object.
const composeResourceObject = "object"

// openComposeWorkload wires the compose provider for composeProject over its store, the daemon
// connected; what it opens goes on opened.
func openComposeWorkload(
	ctx context.Context, cfg config.Config, store storages.Storager,
	composeProject model.DockerComposeProjectName, opened *closers,
) (provider.Workload, error) {
	// Where compose's own progress goes, on the logger initRoot resolved.
	composeOutput := newComposeProgress(
		composeProgressMode(ctx, cfg.Compose.Progress, runProgressMode == progressModeTTY), *log.Ctx(ctx),
	)
	daemon, err := connectDaemon(cfg.Docker, composeOutput.stream, opened)
	if err != nil {
		return nil, err
	}
	// compose asks before recreating a volume; unanswered, it would ask on stdout.
	prompter := composeclient.NewPrompter(*log.Ctx(ctx))
	composeAPI, err := dockercompose.NewComposeService(daemon.cli,
		dockercompose.WithEventProcessor(composeOutput.processor),
		dockercompose.WithPrompt(prompter.Prompt),
	)
	if err != nil {
		return nil, fmt.Errorf("initialise compose: %w", err)
	}
	profile, err := readRuntimeProfile(ctx, daemon)
	if err != nil {
		return nil, err
	}

	// The tree every cdc repository lives in — one directory per volume, named by its ObjectID,
	// beside the savepoint directories. Resolved once and handed to both sides, so "the server
	// serves exactly the tree cdc writes into" is visible in one place.
	resticRoot, err := store.SubStorage(cdc.NamespaceRepos, true)
	if err != nil {
		return nil, fmt.Errorf("initialise restic repository root: %w", err)
	}

	// Under tcp the server listens for the whole process, so a bad address fails up front.
	cdcTransport, err := openCDCTransport(ctx, config.CDCWithDefaults(cfg.Backends.CDC), profile.engine, resticRoot)
	if err != nil {
		return nil, err
	}
	opened.addFunc(cdcTransport.Close)

	backendRegistry, err := newBackendRegistry(cfg, resticRoot, profile.volumes, profile.engine, cdcTransport.transport)
	if err != nil {
		return nil, err
	}
	origin := model.LayerOrigin{
		ArcaveProject:  model.ArcaveProjectName(cfg.Project.Name),
		ComposeProject: composeProject,
	}
	imageBackend, err := newImageBackend(store, origin, profile.engine)
	if err != nil {
		return nil, err
	}

	// One runner for the capture and the rollback: it routes compose's events to the call's step.
	composeRunner := composeclient.NewClient(composeAPI, composeclient.Options{
		WaitTimeout:     cfg.Compose.WaitTimeout,
		StopTimeout:     cfg.Compose.StopTimeout,
		Events:          composeOutput.events,
		Prompter:        prompter,
		SinkFromContext: newComposeEventSink,
	})
	return compose.New(compose.Deps{
		// One store for the records and the data, so every record travels with its data.
		SavepointRepo:    sprepo.New(store),
		ComposeInspector: introspect.NewInspector(profile.api, composeProject),
		ImageBackend:     imageBackend,
		ComposeRunner:    composeRunner,
		ContainerControl: profile.engine,
		VolumeRemover:    profile.engine,
		// The per-replica half of a layer restore: what compose's one image per
		// service cannot express, the engine rebuilds container by container.
		ContainerRebuilder: profile.engine,
		ImageLookup:        profile.engine,
		ProjectAssembler:   profile.assembler,
		SpecCodec:          composespec.NewCodec(),
		BackendRegistry:    backendRegistry,
		CaptureDefaults:    newCaptureDefaults(cfg),
		LeftoverVolumes:    cfg.Rollback.Policy(),
		DaemonVersion:      profile.version,
		MinDaemonMajor:     profile.minDaemonMajor,
		Store:              store,
		Concurrency:        cfg.Common.Concurrency,
		RunnerInfo:         newRunnerInfo(),
	}), nil
}

// newComposeSavepoints is the compose provider's savepoint records in a compose project's store.
func newComposeSavepoints(store storages.Storager) provider.Savepoints {
	return compose.NewSavepoints(sprepo.New(store))
}

// newComposeSession is the compose provider's side of session id, reached with no daemon.
func newComposeSession(arcaveProjectStore storages.Storager, id model.SessionID) provider.Session {
	return compose.NewSession(compose.SessionDeps{
		Store:                  arcaveProjectStore,
		Session:                id,
		NewSavepointRepository: newSavepointRepository,
	})
}

// openComposeGarbageSession is the compose provider's side of session id with its garbage, the
// daemon connected; whether it answers is the restored-layer collector's probe.
func openComposeGarbageSession(
	cfg config.Config, arcaveProjectStore storages.Storager, id model.SessionID, opened *closers,
) (provider.Session, error) {
	// A docker.* the config gets wrong fails every command alike.
	daemon, err := connectDaemon(cfg.Docker, io.Discard, opened)
	if err != nil {
		return nil, err
	}
	engine := docker.NewEngine(daemon.api)
	return compose.NewSession(compose.SessionDeps{
		Store:                  arcaveProjectStore,
		Session:                id,
		NewSavepointRepository: newSavepointRepository,
		Garbage: &compose.GarbageDeps{
			Daemon:        engine,
			ArcaveProject: model.ArcaveProjectName(cfg.Project.Name),
			Concurrency:   cfg.Common.Concurrency,
			ResticInspectors: resticInspectorOpener{
				cfg:    config.CDCWithDefaults(cfg.Backends.CDC),
				daemon: engine,
			},
			ResticPruners: resticPrunerOpener{
				cfg:    config.CDCWithDefaults(cfg.Backends.CDC),
				daemon: engine,
			},
		},
	}), nil
}

// openComposeContainers reads composeProject's containers off the daemon, as status does. Nothing
// here runs compose, so the CLI's progress channel goes nowhere. No runtime profile: the container
// list and the compat inspect read alike on every engine.
func openComposeContainers(
	cfg config.Docker, composeProject model.DockerComposeProjectName, opened *closers,
) (*introspect.Inspector, error) {
	daemon, err := connectDaemon(cfg, io.Discard, opened)
	if err != nil {
		return nil, err
	}
	return introspect.NewInspector(daemon.api, composeProject), nil
}

// composeProgress is where compose's own progress goes.
type composeProgress struct {
	// processor is what compose reports its events to.
	processor api.EventProcessor
	// stream is the CLI's error stream, which compose writes beside its events.
	stream io.Writer
	// events re-emits the events as log records, or on a step; nil when compose renders them itself.
	events *composeclient.EventLogger
}

// newComposeProgress chooses compose's renderer: its own on stderr, nothing, or by default the
// logger re-emitting its events as log records, which a call's step can take them from.
func newComposeProgress(mode string, logger zerolog.Logger) composeProgress {
	switch mode {
	case config.ProgressQuiet:
		// Discarding the stream too covers the warnings compose writes outside the UI.
		return composeProgress{
			processor: display.Quiet(),
			stream:    io.Discard,
		}
	case config.ProgressTTY:
		processor := display.Plain(os.Stderr)
		if isTerminal(os.Stderr) {
			processor = display.Full(os.Stderr, os.Stderr, false)
		}
		return composeProgress{
			processor: processor,
			stream:    os.Stderr,
		}
	default:
		events := composeclient.NewEventLogger(logger)
		return composeProgress{
			processor: events,
			stream:    io.Discard,
			events:    events,
		}
	}
}

// composeEventSink puts compose's events on the progress step of the call, a resource per object.
type composeEventSink struct {
	step *progress.StepReporter
}

// newComposeEventSink is the sink of the step ctx carries; nil under a mute one, so the events
// stay in the log.
func newComposeEventSink(ctx context.Context) composeclient.EventSink {
	step := progress.Current(ctx)
	if step.IsMute() {
		return nil
	}
	return composeEventSink{step: step}
}

// Event reports one event as its object's line: in flight, settled, or a warning.
func (s composeEventSink) Event(evt composeclient.Event) {
	kind := strings.ToLower(evt.Kind)
	if kind == "" {
		kind = composeResourceObject
	}
	var (
		resource = progress.Resource{
			Kind: progress.ResourceKind(kind),
			ID:   evt.Name,
		}
		msg = strings.TrimSpace(evt.Status + " " + evt.Text)
	)
	switch evt.State {
	case composeclient.EventWorking:
		s.step.UpdateResource(resource, msg)
	case composeclient.EventDone:
		s.step.SettleResource(resource, msg)
	default:
		s.step.WarnResource(resource, msg)
	}
}

// composeProgressMode keeps compose's own renderer off a terminal arcave's tree is drawn on:
// two renderers on one stderr tear each other's frames.
func composeProgressMode(ctx context.Context, mode string, drawing bool) string {
	if !drawing || mode != config.ProgressTTY {
		return mode
	}
	log.Ctx(ctx).Warn().Msg("compose.progress: tty is ignored while arcave draws its own progress; " +
		"compose's progress goes to the log, pass --progress log to keep compose's renderer")
	return config.ProgressLog
}

// daemonConnection is one connection to the daemon, with no request sent over it yet.
type daemonConnection struct {
	// cli is what compose runs on, over api, so compose and the engine reach one daemon.
	cli *command.DockerCli
	api client.APIClient
}

// connectDaemon builds the connection; its closer goes on opened.
func connectDaemon(cfg config.Docker, progressOut io.Writer, opened *closers) (daemonConnection, error) {
	daemon, err := newDockerClient(cfg, progressOut)
	if err != nil {
		return daemonConnection{}, err
	}
	opened.add("docker client", daemon.api)
	return daemon, nil
}

// newDockerClient resolves cfg into the docker/cli instance compose runs on and its API client.
// progressOut becomes the CLI's error stream once the endpoint resolves.
func newDockerClient(cfg config.Docker, progressOut io.Writer) (daemonConnection, error) {
	clientOpts, err := newDockerClientOptions(cfg)
	if err != nil {
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}

	// DOCKER_API_VERSION is the only seam docker/cli leaves for pinning the API
	// version. Safe to set in-process: arcave spawns no children, so this configures
	// arcave's own client and nothing else.
	if cfg.APIVersion != "" {
		if err := os.Setenv(client.EnvOverrideAPIVersion, cfg.APIVersion); err != nil {
			return daemonConnection{}, fmt.Errorf("pin docker api version %q: %w", cfg.APIVersion, err)
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
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}
	if err := dockerCli.Initialize(clientOpts); err != nil {
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}

	// Initialize can log a benign warning of its own (a malformed
	// ~/.docker/config.json), so from here on the buffer holds only the resolution.
	initErr.Reset()

	// Resolve the endpoint now, while a failure is still arcave's to report: Client() runs the same
	// sync.Once but prints the cause and exits.
	dockerCli.DockerEndpoint()
	if initErr.Len() > 0 {
		return daemonConnection{}, fmt.Errorf("connect to docker engine (docker.host %q, docker.context %q): %s",
			cfg.Host, cfg.Context, strings.TrimSpace(initErr.String()))
	}
	apiClient := dockerCli.Client()

	// Resolution succeeded, so the error stream becomes compose's progress channel.
	if err := command.WithErrorStream(progressOut)(dockerCli); err != nil {
		closeQuietly(context.Background(), "docker client", apiClient)
		return daemonConnection{}, fmt.Errorf("initialise docker client: %w", err)
	}
	return daemonConnection{
		cli: dockerCli,
		api: apiClient,
	}, nil
}

func newDockerClientOptions(cfg config.Docker) (*flags.ClientOptions, error) {
	if cfg.Host != "" && cfg.Context != "" {
		return nil, fmt.Errorf("docker.host and docker.context are mutually exclusive: %q already names a daemon, so remove one of the two", cfg.Context)
	}

	opts := flags.NewClientOptions()
	if cfg.Host != "" {
		opts.Hosts = []string{cfg.Host}
	}
	opts.Context = cfg.Context

	resolved, err := withDockerTLSEnv(cfg)
	if err != nil {
		return nil, err
	}
	tlsOpts, useTLS, err := newDockerTLSOptions(resolved)
	if err != nil {
		return nil, err
	}
	if useTLS {
		opts.TLS = true
		opts.TLSVerify = !resolved.SkipTLSVerify
		opts.TLSOptions = tlsOpts
	}
	return opts, nil
}

// withDockerTLSEnv takes the cert path from DOCKER_CERT_PATH under DOCKER_TLS_VERIFY, only beside
// a tcp DOCKER_HOST the config does not override and with no TLS file named in the config. The
// variable with no certificate to verify against is refused: it would stay plain http.
func withDockerTLSEnv(cfg config.Docker) (config.Docker, error) {
	if cfg.Host != "" || cfg.Context != "" || !isTCPHost(os.Getenv(client.EnvOverrideHost)) {
		return cfg, nil
	}
	if os.Getenv(client.EnvTLSVerify) == "" || cfg.CertPath != "" || cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" {
		return cfg, nil
	}
	certPath := os.Getenv(client.EnvOverrideCertPath)
	if certPath == "" && !cfg.SkipTLSVerify {
		return config.Docker{}, errors.New(client.EnvTLSVerify + " is set but " + client.EnvOverrideCertPath + " is not, so the daemon would be reached over plain http: set it, or docker.ca_file or docker.cert_path")
	}
	cfg.CertPath = certPath
	return cfg, nil
}

// isTCPHost reports whether docker/cli reads host as tcp, as it does one with no scheme. One it
// cannot parse is no tcp host: docker/cli refuses it itself.
func isTCPHost(host string) bool {
	parsed, err := dockeropts.ParseHost(false, host)
	return err == nil && strings.HasPrefix(parsed, "tcp://")
}

// newDockerTLSOptions resolves the docker.* TLS fields into docker/cli's TLSOptions and reports
// whether any TLS material was configured. A half-set cert_file/key_file pair is refused here:
// docker/cli would surface it only as an opaque handshake failure.
func newDockerTLSOptions(cfg config.Docker) (*tlsconfig.Options, bool, error) {
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
	if caFile == "" && certFile == "" && !cfg.SkipTLSVerify {
		return nil, false, nil
	}

	return &tlsconfig.Options{
		CAFile:             caFile,
		CertFile:           certFile,
		KeyFile:            keyFile,
		InsecureSkipVerify: cfg.SkipTLSVerify,
	}, true, nil
}

// tlsFile resolves one TLS file path the way the docker CLI does: an explicit
// path wins, then CertPath/name; neither set leaves it empty.
func tlsFile(explicit, certPath, name string) string {
	if explicit != "" {
		return explicit
	}
	if certPath != "" {
		return filepath.Join(certPath, name)
	}
	return ""
}

// runtimeProfile is everything that differs by the engine behind the daemon.
type runtimeProfile struct {
	// api is the daemon's Docker API, answering as Docker would; introspection and engine read through it.
	api client.APIClient
	// engine is what a workflow reaches the daemon through, but for a volume's stream.
	engine *docker.Engine
	// volumes is how a volume leaves the daemon: engine itself, or Podman's export over it.
	volumes fullcopy.VolumeStreamer
	// version is the version the engine reports; minDaemonMajor judges it.
	version string
	// minDaemonMajor is the oldest version of the engine a workflow runs on; 0 requires none.
	minDaemonMajor int
	// assembler leaves out of a spec what the engine stamps by itself.
	assembler *composespec.Assembler
}

// readRuntimeProfile asks the daemon which engine it is and chooses the profile for it.
func readRuntimeProfile(ctx context.Context, daemon daemonConnection) (runtimeProfile, error) {
	runtime, info, err := readEngine(ctx, daemon.api)
	if err != nil {
		return runtimeProfile{}, err
	}
	return newRuntimeProfile(runtime, info, daemon.api, daemon.cli.DockerEndpoint())
}

// readEngine asks the daemon which engine it is and what `docker info` says of it.
func readEngine(ctx context.Context, api client.APIClient) (model.ContainerRuntime, system.Info, error) {
	runtime, err := docker.ReadRuntime(ctx, api)
	if err != nil {
		return model.ContainerRuntime{}, system.Info{}, err
	}
	info, err := api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return model.ContainerRuntime{}, system.Info{}, fmt.Errorf("read the daemon info: %w", err)
	}
	return runtime, info.Info, nil
}

// newRuntimeProfile chooses everything that differs by the engine behind the daemon; libpod
// is reached through api's own dialer, over TLS when the endpoint is.
func newRuntimeProfile(
	runtime model.ContainerRuntime, info system.Info, api client.APIClient,
	endpoint dockercontext.Endpoint,
) (runtimeProfile, error) {
	switch runtime.Kind {
	case model.ContainerRuntimePodman:
		if isHTTPHost(endpoint.Host) {
			return runtimeProfile{}, fmt.Errorf("docker endpoint %q: the Docker client cannot attach to a container over an http:// or https:// host, which a Podman volume export needs: name it tcp://", endpoint.Host)
		}
		libpodClient, err := libpod.NewClient(api, libpod.Options{Version: runtime.Version})
		if err != nil {
			return runtimeProfile{}, fmt.Errorf("reach libpod: %w", err)
		}
		podmanAPI := podman.NewAPI(api, libpodClient)
		engine := docker.NewEngine(podmanAPI)
		return runtimeProfile{
			api:            podmanAPI,
			engine:         engine,
			volumes:        podman.NewEngine(engine),
			version:        runtime.Version,
			minDaemonMajor: 0,
			assembler:      composespec.NewAssembler(composespec.NewPodmanStamps(info)),
		}, nil
	default:
		if model.IsDockerDesktop(info) {
			api = dockerdesktop.NewAPI(api)
		}
		engine := docker.NewEngine(api)
		return runtimeProfile{
			api:            api,
			engine:         engine,
			volumes:        engine,
			version:        runtime.Version,
			minDaemonMajor: dedupcopy.MinDaemonMajor,
			assembler:      composespec.NewAssembler(composespec.NewDockerStamps(info)),
		}, nil
	}
}

// isHTTPHost reports whether host, which only a context can spell so, names http or https.
func isHTTPHost(host string) bool {
	return strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://")
}

// openedCDCTransport is how cdc's containers reach one tree of repositories.
type openedCDCTransport struct {
	transport cdc.Transport
	// server is the tcp transport's listener; nil under stdio.
	server *echo.Echo
}

// Close stops the tcp transport's server, if any.
func (o openedCDCTransport) Close(ctx context.Context) { shutdownResticServer(ctx, o.server) }

// hostLoopbackReader is what choosing the tcp transport's network asks the daemon.
type hostLoopbackReader interface {
	HostLoopbackShared(ctx context.Context) (bool, error)
}

// openCDCTransport opens backends.cdc.transport over resticRoot: a stream server
// under stdio, a listening server and the URL base to dial it at under tcp.
func openCDCTransport(
	ctx context.Context, cfg config.CDCBackend, daemon hostLoopbackReader, resticRoot storages.Storager,
) (openedCDCTransport, error) {
	switch cfg.Transport {
	case config.CDCTransportStdio:
		return openedCDCTransport{
			transport: cdc.StdioTransport{
				Server: rest.NewConnServer(ctx, repo.NewResolver(resticRoot)),
			},
		}, nil
	case config.CDCTransportTCP:
		return openTCPTransport(ctx, cfg, daemon, resticRoot)
	default:
		return openedCDCTransport{}, fmt.Errorf("unsupported backends.cdc.transport %q", cfg.Transport)
	}
}

// openTCPTransport starts the server restic containers dial, and names the URL base
// they dial it at.
func openTCPTransport(
	ctx context.Context, cfg config.CDCBackend, daemon hostLoopbackReader, resticRoot storages.Storager,
) (openedCDCTransport, error) {
	hostNetwork, containerHost, err := resolveCDCNetwork(ctx, cfg, daemon)
	if err != nil {
		return openedCDCTransport{}, err
	}
	server := rest.NewEcho(ctx, repo.NewResolver(resticRoot))
	port, err := rest.Serve(server, cfg.ServerAddress)
	if err != nil {
		return openedCDCTransport{}, fmt.Errorf("start the restic server: %w", err)
	}
	return openedCDCTransport{
		transport: cdc.TCPTransport{
			RepoURLBase: rest.RepositoryURLBase(cfg.ServerScheme, containerHost, port),
			HostNetwork: hostNetwork,
		},
		server: server,
	}, nil
}

// resolveCDCNetwork turns backends.cdc.network_mode into how the tcp
// transport's restic containers reach the server: whether they join the host's network namespace, and
// the host their repository URL names. Auto probes the daemon, but mispicks
// host networking for a remote Linux daemon — bridge is the escape hatch there.
func resolveCDCNetwork(
	ctx context.Context, cfg config.CDCBackend, daemon hostLoopbackReader,
) (hostNetwork bool, containerHost string, err error) {
	switch cfg.NetworkMode {
	case config.NetworkModeHost:
		return true, "127.0.0.1", nil
	case config.NetworkModeBridge:
		return false, cfg.ContainerHost, nil
	default: // config.NetworkModeAuto — Validate admits nothing else.
		shared, err := daemon.HostLoopbackShared(ctx)
		if err != nil {
			return false, "", fmt.Errorf("resolve backends.cdc.network_mode %q: %w", cfg.NetworkMode, err)
		}
		if shared {
			return true, "127.0.0.1", nil
		}
		return false, cfg.ContainerHost, nil
	}
}

// newCDCOptions translates the backends.cdc section into the backend's own options.
func newCDCOptions(cfg config.CDCBackend) cdc.Options {
	return cdc.Options{
		Image:       cfg.Image,
		Incremental: cfg.RestoreMode == config.RestoreModeIncremental,
		Overwrite:   cfg.Overwrite,
	}
}

// shutdownResticServer stops server gracefully, on a deadline detached from ctx: restic
// reads a severed connection as a repository error. A nil server is a no-op.
func shutdownResticServer(ctx context.Context, server *echo.Echo) {
	if server == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resticShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("shut down restic server")
	}
}

// newBackendRegistry builds every volume backend: a rollback restores through the backend its
// record names, so cdc is wired, its section's defaults included, whatever snapshot.backend says.
func newBackendRegistry(
	cfg config.Config, resticRoot storages.Storager,
	streamer fullcopy.VolumeStreamer, runner cdc.VolumeRunner, transport cdc.Transport,
) (model.SnapshotBackendRegistry, error) {
	backendRegistry, err := compose.NewBackendRegistry(
		fullcopy.NewFactory(streamer, fullcopy.Compression(cfg.Backends.FullCopy.Compression)),
		cdc.NewFactory(cdc.Dependencies{
			Runner:         runner,
			Transport:      transport,
			RepositoryRoot: resticRoot,
			Options:        newCDCOptions(config.CDCWithDefaults(cfg.Backends.CDC)),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build snapshot backend registry: %w", err)
	}
	return backendRegistry, nil
}

// newImageBackend builds the image backend over both archivers a writable layer may name;
// images are always deduplicated, which needs a daemon of Docker 25 or newer.
func newImageBackend(
	composeProjectStore storages.Navigator, origin model.LayerOrigin, engine *docker.Engine,
) (*imagebackend.Copier, error) {
	blobStore, err := composeProjectStore.SubStorage(dedupcopy.NamespaceBlobs, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at the shared blob store: %w", err)
	}
	dedup := dedupcopy.New(blobStore)
	return imagebackend.NewCopier(composeProjectStore, origin, engine, map[model.ImageBackendKind]imagebackend.ImageArchiver{
		model.ImageBackendDedupCopy: dedup,
		model.ImageBackendFullCopy:  imagefullcopy.New(),
	}, dedup), nil
}

// newCaptureDefaults is what the config says of a unit no arcave.* label names. The
// capture keys are read as ApplyDefaults left them: every one set.
func newCaptureDefaults(cfg config.Config) capturepolicy.CaptureDefaults {
	return capturepolicy.CaptureDefaults{
		VolumeBackend:        cfg.Snapshot.BackendKind(),
		WritableLayerBackend: cfg.WritableLayers.BackendKind(),
		Mode:                 model.CaptureMode(cfg.Capture.Mode),
		SavepointCapture: capturepolicy.SavepointCaptureSettings{
			WritableLayers:   utils.Value(cfg.Capture.WritableLayers),
			Images:           utils.Value(cfg.Capture.Images),
			NamedVolumes:     utils.Value(cfg.Capture.Volumes.Named),
			BindVolumes:      utils.Value(cfg.Capture.Volumes.Bind),
			AnonymousVolumes: utils.Value(cfg.Capture.Volumes.Anonymous),
		},
	}
}

// newSavepointLoader is the savepoint repository of a compose project's store, as status reads it.
func newSavepointLoader(store storages.Storager) status.SavepointLoader {
	return sprepo.New(store)
}

// newSavepointRepository is the savepoint repository of a compose project's store, as the provider
// reads it with no daemon.
func newSavepointRepository(store storages.Storager) compose.SavepointRepository {
	return sprepo.New(store)
}

// gcDaemon is all a gc asks of the daemon. Nothing it reads differs by engine, so it runs over the
// client itself; a call added here must depend on nothing podman.API changes.
type gcDaemon interface {
	Ping(ctx context.Context) error
	ListLayerImages(ctx context.Context) ([]model.TaggedImage, error)
	ListContainerImages(ctx context.Context) ([]model.ContainerImage, error)
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
	RunOneShot(ctx context.Context, spec model.OneShotSpec) error
	HostLoopbackShared(ctx context.Context) (bool, error)
}

// openedRestic is one compose project's restic repositories, served to restic containers.
type openedRestic struct {
	// root is the repositories as objects, rooted at cdc.NamespaceRepos.
	root storages.Storager
	openedCDCTransport
}

// openResticRepositories opens the transport over store's repositories, as openSavepointService
// serves them to the cdc backend; the transport's Close releases it.
func openResticRepositories(
	ctx context.Context, cfg config.CDCBackend, daemon hostLoopbackReader, store storages.Navigator,
) (openedRestic, error) {
	resticRoot, err := store.SubStorage(cdc.NamespaceRepos, true)
	if err != nil {
		return openedRestic{}, fmt.Errorf("open restic repository root: %w", err)
	}
	transport, err := openCDCTransport(ctx, cfg, daemon, resticRoot)
	if err != nil {
		return openedRestic{}, err
	}
	return openedRestic{
		root:               resticRoot,
		openedCDCTransport: transport,
	}, nil
}

// resticInspectorOpener serves one compose project's restic repositories to a cdc.Inspector.
type resticInspectorOpener struct {
	cfg    config.CDCBackend
	daemon gcDaemon
}

// Open opens the transport over store's repositories; the inspector's Close releases it.
func (o resticInspectorOpener) Open(ctx context.Context, store storages.Navigator) (compose.ResticInspector, error) {
	served, err := openResticRepositories(ctx, o.cfg, o.daemon, store)
	if err != nil {
		return nil, err
	}
	return resticInspector{
		Inspector:          cdc.NewInspector(o.daemon, served.transport, newCDCOptions(o.cfg)),
		openedCDCTransport: served.openedCDCTransport,
	}, nil
}

// resticInspector is a cdc.Inspector with the transport it reads through.
type resticInspector struct {
	*cdc.Inspector
	openedCDCTransport
}

// resticPrunerOpener serves one compose project's restic repositories to a cdc.Pruner.
type resticPrunerOpener struct {
	cfg    config.CDCBackend
	daemon gcDaemon
}

// Open opens the transport over store's repositories; the pruner's Close releases it.
func (o resticPrunerOpener) Open(ctx context.Context, store storages.Navigator) (compose.ResticPruner, error) {
	served, err := openResticRepositories(ctx, o.cfg, o.daemon, store)
	if err != nil {
		return nil, err
	}
	return resticPruner{
		Pruner:             cdc.NewPruner(o.daemon, served.root, served.transport, newCDCOptions(o.cfg)),
		openedCDCTransport: served.openedCDCTransport,
	}, nil
}

// resticPruner is a cdc.Pruner with the transport it prunes through.
type resticPruner struct {
	*cdc.Pruner
	openedCDCTransport
}
