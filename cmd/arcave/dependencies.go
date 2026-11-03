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

	"filippo.io/age"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/api"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	composeprogress "github.com/docker/compose/v2/pkg/progress"
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
	"github.com/greenmaskio/arcave/internal/repository/currentsession"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	sessionrepo "github.com/greenmaskio/arcave/internal/repository/session"
	"github.com/greenmaskio/arcave/internal/service/backend/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	imagefullcopy "github.com/greenmaskio/arcave/internal/service/backend/imagecopy/fullcopy"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/layertag"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/ociblob"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/ociimage"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/resticrepo"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/savepointdir"
	"github.com/greenmaskio/arcave/internal/service/gc/eraser"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/service/sessionlock"
	"github.com/greenmaskio/arcave/internal/service/storekey"
	"github.com/greenmaskio/arcave/internal/storefactory"
	"github.com/greenmaskio/arcave/internal/utils"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// resticShutdownTimeout bounds the restic server's graceful shutdown.
const resticShutdownTimeout = 5 * time.Second

// encryptionMarkerSettle is how long a host that marks an arcave project encrypted waits
// before it reads the marker back, as a session lock claim does.
var encryptionMarkerSettle = sessionlock.DefaultSettle

// composeResourceObject is the resource kind of a compose event whose id names no kind of object.
const composeResourceObject = "object"

// dependencies is the composition root's bag of per-command collaborators.
type dependencies struct {
	storeRoot           storages.Storager
	composeProjectStore storages.Storager
	progressWriter      io.WriteCloser
	dockerCli           *command.DockerCli
	dockerClient        client.APIClient
	composeService      api.Compose
	engine              *docker.Engine
	composeRunner       *compose.Client
	// cdcTransport is how cdc's containers reach their repositories, closed by Close.
	cdcTransport openedCDCTransport
	service      *core.Service
	printer      resultPrinter
}

// newDependencies wires the savepoint workflows for the project rooted at projectPath, in the
// session sessionRef resolves to on the store it opens. Every store arrives from storefactory
// already rooted.
func newDependencies(
	ctx context.Context, projectPath string, cfg config.Config,
	sessionRef string, composeProject model.DockerComposeProjectName,
) (*dependencies, error) {
	// One unwind for every failure path: whatever is open is released newest first.
	var opened partial
	defer func() { opened.releaseOnFailure(ctx) }()

	arcaveProject, err := openArcaveProjectStore(ctx, cfg, projectPath)
	if err != nil {
		return nil, err
	}
	storeRoot, arcaveProjectStore := arcaveProject.root, arcaveProject.store
	opened.add("store root", storeRoot)

	sessions := sessionrepo.New(arcaveProjectStore)
	session, err := newSessionService(projectPath, arcaveProjectStore, sessions).ResolveSession(ctx, sessionRef)
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}

	// One store for the whole compose project: savepoint records, fullcopy captures and a
	// cdc repository root alike, rooted inside the session that they die with.
	sessionStore, err := storefactory.Session(arcaveProjectStore, session.ID)
	if err != nil {
		return nil, fmt.Errorf("initialise session store: %w", err)
	}
	composeProjectStore, err := storefactory.ComposeProject(sessionStore, composeProject)
	if err != nil {
		return nil, fmt.Errorf("initialise compose project store: %w", err)
	}

	// Where compose's own progress goes, on the logger initRoot resolved.
	progressWriter, composeEvents := newProgressWriter(
		composeProgressMode(ctx, cfg.Compose.Progress, runProgressMode == progressModeTTY), *log.Ctx(ctx))
	opened.add("compose progress writer", progressWriter)

	// One docker/cli instance is the single daemon connection both halves of
	// client/docker are built over, so they cannot target different daemons. Its
	// closers belong to the returned dependencies — never deferred here.
	dockerCli, dockerClientAPI, err := newDockerClient(cfg.Docker, progressWriter)
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
		WaitTimeout:     cfg.Compose.WaitTimeout,
		StopTimeout:     cfg.Compose.StopTimeout,
		Events:          composeEvents,
		SinkFromContext: newComposeEventSink,
	})

	// The effective cdc section, defaults included even where the project carries
	// no block: a rollback restores through the backend its record names, so cdc
	// is wired whatever snapshot.backend says.
	cdcCfg := config.CDCWithDefaults(cfg.Backends.CDC)

	// The tree every cdc repository lives in — one directory per volume, named by
	// its ObjectID, beside the compose project's savepoint directories. Resolved once and
	// handed to both sides, so "the server serves exactly the tree cdc writes
	// into" is visible in one place rather than kept by two call sites agreeing.
	resticRoot, err := composeProjectStore.SubStorage(cdc.NamespaceRepos, true)
	if err != nil {
		return nil, fmt.Errorf("initialise restic repository root: %w", err)
	}

	// Under tcp the server listens for the whole process, so a bad address fails up front.
	cdcTransport, err := openCDCTransport(ctx, cdcCfg, engine, resticRoot)
	if err != nil {
		return nil, err
	}
	if cdcTransport.server != nil {
		opened.add("restic server", cdcTransport.server)
	}

	// The one place a volume's Backend becomes an instance, and the only place
	// that names the engine — through the seam its backend declares.
	backendRegistry, err := core.NewSnapshotBackendRegistry(
		fullcopy.NewFactory(engine),
		cdc.NewFactory(cdc.Dependencies{
			Runner:         engine,
			Transport:      cdcTransport.transport,
			RepositoryRoot: resticRoot,
			Options:        newCDCOptions(cdcCfg, arcaveProject.resticPassword),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("build snapshot backend registry: %w", err)
	}

	origin := model.LayerOrigin{
		ArcaveProject:  model.ArcaveProjectName(cfg.Project.Name),
		ComposeProject: composeProject,
	}
	imageBackend, err := newImageBackend(composeProjectStore, origin, engine)
	if err != nil {
		return nil, err
	}

	sessionLocker, err := newSessionLocker(arcaveProjectStore, session.ID)
	if err != nil {
		return nil, fmt.Errorf("open the session lock: %w", err)
	}

	// One repository over the one store, so every record travels with its data. The
	// locker is bound to the project root here, so the service never names a path.
	svc := core.New(core.Deps{
		SavepointRepo: sprepo.New(composeProjectStore),
		ProjectLocker: lock.New(projectPath),
		// The session's lock in the store, which the flock beside it cannot be across hosts.
		SessionLocker:    sessionLocker,
		ComposeInspector: inspector,
		ProjectAssembler: assembler,
		SpecCodec:        codec,
		ComposeRunner:    composeRunner,
		VolumeRemover:    engine,
		ImageBackend:     imageBackend,
		MinDaemonMajor:   dedupcopy.MinDaemonMajor,
		Concurrency:      cfg.Common.Concurrency,
		// The per-replica half of a layer restore: what compose's one image per
		// service cannot express, the engine rebuilds container by container.
		ContainerRebuilder: engine,
		ImageLookup:        engine,
		BackendRegistry:    backendRegistry,
		// The marker an interrupted rollback leaves behind, host-local beside the lock.
		Inflight:        inflightrepo.New(projectPath),
		Store:           composeProjectStore,
		CaptureDefaults: newCaptureDefaults(cfg),
		Session:         session.ID,
		ComposeProject:  composeProject,
		// Re-checked under the lock: a session deleted since --session was resolved.
		Sessions:   sessions,
		RunnerInfo: newRunnerInfo(),
	})
	opened.keep()
	return &dependencies{
		storeRoot:           storeRoot,
		composeProjectStore: composeProjectStore,
		progressWriter:      progressWriter,
		dockerCli:           dockerCli,
		dockerClient:        dockerClientAPI,
		composeService:      composeService,
		engine:              engine,
		composeRunner:       composeRunner,
		cdcTransport:        cdcTransport,
		service:             svc,
		printer:             loadRunOutput(),
	}, nil
}

// openedCDCTransport is how cdc's containers reach one tree of repositories.
type openedCDCTransport struct {
	transport cdc.Transport
	// server is the tcp transport's listener; nil under stdio.
	server *echo.Echo
}

// Close stops the tcp transport's server, if any.
func (o openedCDCTransport) Close(ctx context.Context) { shutdownResticServer(ctx, o.server) }

// openCDCTransport opens backends.cdc.transport over resticRoot: a stream server
// under stdio, a listening server and the URL base to dial it at under tcp.
func openCDCTransport(
	ctx context.Context, cfg config.CDCBackend, engine *docker.Engine, resticRoot storages.Storager,
) (openedCDCTransport, error) {
	switch cfg.Transport {
	case config.CDCTransportStdio:
		return openedCDCTransport{
			transport: cdc.StdioTransport{
				Server: rest.NewConnServer(ctx, repo.NewResolver(resticRoot)),
			},
		}, nil
	case config.CDCTransportTCP:
		return openTCPTransport(ctx, cfg, engine, resticRoot)
	default:
		return openedCDCTransport{}, fmt.Errorf("unsupported backends.cdc.transport %q", cfg.Transport)
	}
}

// openTCPTransport starts the server restic containers dial, and names the URL base
// they dial it at.
func openTCPTransport(
	ctx context.Context, cfg config.CDCBackend, engine *docker.Engine, resticRoot storages.Storager,
) (openedCDCTransport, error) {
	hostNetwork, containerHost, err := resolveCDCNetwork(ctx, cfg, engine)
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

// newCDCOptions translates the backends.cdc section into the backend's own options, with the
// password the arcave project's repositories open with.
func newCDCOptions(cfg config.CDCBackend, resticPassword string) cdc.Options {
	return cdc.Options{
		Image:       cfg.Image,
		Incremental: cfg.RestoreMode == config.RestoreModeIncremental,
		Overwrite:   cfg.Overwrite,
		Password:    resticPassword,
	}
}

// partial tracks what a half-finished wiring has opened. releaseOnFailure is
// deferred once and does nothing after keep, which the success path calls.
type partial struct {
	closers []namedCloser
	kept    bool
}

type namedCloser struct {
	name   string
	closer io.Closer
}

func (p *partial) add(name string, closer io.Closer) {
	p.closers = append(p.closers, namedCloser{name, closer})
}

func (p *partial) keep() { p.kept = true }

func (p *partial) releaseOnFailure(ctx context.Context) {
	if p.kept {
		return
	}
	for i := len(p.closers) - 1; i >= 0; i-- {
		closeQuietly(ctx, p.closers[i].name, p.closers[i].closer)
	}
}

// Close releases the cdc transport, the Docker engine, the progress writer and
// the store this root owns; only the store's error is returned. The progress
// writer is closed after the engine, so a line compose left unterminated is
// still emitted. dockerClient is not closed — the engine was built over it.
func (d *dependencies) Close(ctx context.Context) error {
	d.cdcTransport.Close(ctx)
	closeQuietly(ctx, "docker engine", d.engine)
	closeQuietly(ctx, "compose progress writer", d.progressWriter)
	return d.storeRoot.Close()
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

// newInitDependencies wires `arcave init`: somewhere to write the config, nothing
// more. Nothing here holds a resource.
func newInitDependencies(projectPath string) (*core.InitService, resultPrinter) {
	return core.NewInitService(projectPath, cfgrepo.New(projectPath)), loadRunOutput()
}

// toolsDependencies is what the tools commands run on: one daemon connection, no store.
type toolsDependencies struct {
	dockerClient client.APIClient
	service      *core.ComposeFileService
	printer      resultPrinter
}

// newToolsDependencies wires the tools commands for composeProject over the daemon docker.* names.
func newToolsDependencies(cfg config.Config, composeProject model.DockerComposeProjectName) (*toolsDependencies, error) {
	// The gate newDependencies applies through storefactory, though no store is opened here.
	if err := storefactory.ValidateComposeProjectName(composeProject); err != nil {
		return nil, err
	}
	// Nothing here runs compose, so the CLI's progress channel goes nowhere.
	_, dockerClientAPI, err := newDockerClient(cfg.Docker, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("initialise docker client: %w", err)
	}
	svc := core.NewComposeFileService(core.ComposeFileDeps{
		ComposeInspector: introspect.NewInspector(dockerClientAPI, composeProject),
		ProjectAssembler: composespec.NewAssembler(),
		SpecCodec:        composespec.NewComposeFileCodec(),
	})
	return &toolsDependencies{
		dockerClient: dockerClientAPI,
		service:      svc,
		printer:      loadRunOutput(),
	}, nil
}

func (d *toolsDependencies) Close() error {
	return d.dockerClient.Close()
}

// sessionDependencies is what the session commands run on: the arcave project's store
// and nothing that reaches the daemon.
type sessionDependencies struct {
	store   storages.Storager
	service *core.SessionService
	printer resultPrinter
}

func newSessionDependencies(ctx context.Context, projectPath string, cfg config.Config) (*sessionDependencies, error) {
	arcaveProject, err := openArcaveProjectStore(ctx, cfg, projectPath)
	if err != nil {
		return nil, err
	}
	svc := newSessionService(projectPath, arcaveProject.store, sessionrepo.New(arcaveProject.store))
	return &sessionDependencies{
		store:   arcaveProject.root,
		service: svc,
		printer: loadRunOutput(),
	}, nil
}

// newSessionService wires the session commands over the arcave project's store.
func newSessionService(
	projectPath string, arcaveProjectStore storages.Storager, sessions *sessionrepo.Repository,
) *core.SessionService {
	return core.NewSessionService(core.SessionDeps{
		Sessions:      sessions,
		Locker:        lock.New(projectPath),
		Inflight:      inflightrepo.New(projectPath),
		Current:       currentsession.New(projectPath),
		Store:         arcaveProjectStore,
		NewSavepoints: newSavepointReader,
		NewSessionLocker: func(id model.SessionID) (model.SessionLocker, error) {
			return newSessionLocker(arcaveProjectStore, id)
		},
		RunnerInfo: newRunnerInfo(),
	})
}

func (d *sessionDependencies) Close() error {
	return d.store.Close()
}

// newSessionLocker opens the lock of session id in the arcave project's store.
func newSessionLocker(arcaveProjectStore storages.Navigator, id model.SessionID) (*sessionlock.Locker, error) {
	store, key, err := storefactory.SessionLock(arcaveProjectStore, id)
	if err != nil {
		return nil, err
	}
	return sessionlock.New(sessionlock.NewFischerSessionLockStore(store, key), id, newRunnerInfo()), nil
}

// newRunnerInfo identifies this process in a heartbeat; an unknown host stays blank.
func newRunnerInfo() heartbeat.RunnerInfo {
	host, err := os.Hostname()
	if err != nil {
		log.Debug().Err(err).Msg("reading the hostname for the heartbeat")
	}
	return heartbeat.RunnerInfo{Host: host, PID: os.Getpid(), ArcaveVersion: version}
}

// openedArcaveProject is the arcave project's store as every workflow reads and writes it.
type openedArcaveProject struct {
	// root is what the caller closes.
	root  storages.Storager
	store storages.Storager
	// resticPassword opens the arcave project's restic repositories.
	resticPassword string
}

// openArcaveProjectStore opens the store root and descends to the arcave project, encrypted to
// encryption.key under encryption.enabled.
func openArcaveProjectStore(ctx context.Context, cfg config.Config, projectPath string) (openedArcaveProject, error) {
	root, err := storefactory.Root(ctx, cfg.Storage, projectPath)
	if err != nil {
		return openedArcaveProject{}, fmt.Errorf("initialise store root: %w", err)
	}
	arcaveProject, err := openArcaveProject(ctx, cfg, root)
	if err != nil {
		if closeErr := root.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("close store root")
		}
		return openedArcaveProject{}, err
	}
	return arcaveProject, nil
}

func openArcaveProject(ctx context.Context, cfg config.Config, root storages.Storager) (openedArcaveProject, error) {
	var (
		plain    storages.Storager
		identity *age.X25519Identity
		marked   bool
		torn     error
		store    storages.Storager
		password string
		err      error
	)
	if plain, err = storefactory.ArcaveProject(root, model.ArcaveProjectName(cfg.Project.Name)); err != nil {
		return openedArcaveProject{}, fmt.Errorf("initialise arcave project store: %w", err)
	}
	markers := encryptionmarker.New(plain)
	if !cfg.Encryption.Enabled {
		if err = requirePlainStore(ctx, markers); err != nil {
			return openedArcaveProject{}, newStoreKeyError(err)
		}
		return openedArcaveProject{
			root:           root,
			store:          plain,
			resticPassword: cdc.DefaultPassword,
		}, nil
	}
	if identity, err = storekey.ParseKey(cfg.Encryption.Key); err != nil {
		return openedArcaveProject{}, newStoreKeyError(err)
	}
	recipient := identity.Recipient().String()
	switch marked, err = requireEncryptedStore(ctx, markers, recipient); {
	case errors.Is(err, model.ErrInvalidEncryptionMarker):
		torn = err
	case err != nil:
		return openedArcaveProject{}, newStoreKeyError(err)
	}
	if !marked {
		if err = markEncryptedStore(ctx, plain, markers, recipient, torn); err != nil {
			return openedArcaveProject{}, newStoreKeyError(err)
		}
	}
	if store, err = storefactory.Encrypt(plain, identity); err != nil {
		return openedArcaveProject{}, err
	}
	if password, err = storekey.NewResticPassword(identity); err != nil {
		return openedArcaveProject{}, err
	}
	return openedArcaveProject{
		root:           root,
		store:          store,
		resticPassword: password,
	}, nil
}

// requirePlainStore refuses an arcave project an encrypted host has marked: model.ErrStoreEncrypted.
func requirePlainStore(ctx context.Context, markers *encryptionmarker.Repository) error {
	_, found, err := markers.Get(ctx)
	if err != nil {
		return err
	}
	if found {
		return model.ErrStoreEncrypted
	}
	return nil
}

// requireEncryptedStore reports whether the arcave project is marked for recipient, refusing
// one marked for another; a marker no host wrote whole is model.ErrInvalidEncryptionMarker.
func requireEncryptedStore(ctx context.Context, markers *encryptionmarker.Repository, recipient string) (bool, error) {
	marker, found, err := markers.Get(ctx)
	switch {
	case err != nil:
		return false, err
	case !found:
		return false, nil
	case marker.Recipient != recipient:
		return false, fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
	}
	return true, nil
}

// markEncryptedStore marks an empty arcave project for recipient — a torn marker over it
// written again — refusing one holding data with torn, else model.ErrStoreUnencrypted, and
// reads the marker back after the settle, so a host racing with another key is refused
// before it writes anything.
func markEncryptedStore(
	ctx context.Context, plain storages.Navigator, markers *encryptionmarker.Repository, recipient string, torn error,
) error {
	holdsData, err := storefactory.HoldsData(ctx, plain, encryptionmarker.FileName)
	if err != nil {
		return err
	}
	if holdsData {
		if torn != nil {
			return torn
		}
		return model.ErrStoreUnencrypted
	}
	if err = markers.Put(ctx, model.NewEncryptionMarker(recipient)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(encryptionMarkerSettle):
	}
	marker, found, err := markers.Get(ctx)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s did not read back", encryptionmarker.FileName)
	}
	if marker.Recipient != recipient {
		return fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
	}
	return nil
}

// newStoreKeyError adds the fix to a store the encryption section does not open.
func newStoreKeyError(err error) error {
	switch {
	case errors.Is(err, model.ErrInvalidStoreKey), errors.Is(err, model.ErrWrongStoreKey):
		return fmt.Errorf("open the store: %w (check encryption.key, ARCAVE_ENCRYPTION_KEY by default)", err)
	case errors.Is(err, model.ErrStoreUnencrypted):
		return fmt.Errorf("open the store: %w: the arcave project was written without encryption, "+
			"and arcave does not encrypt it in place (set encryption.enabled: false, "+
			"or point storage at an empty location)", err)
	case errors.Is(err, model.ErrStoreEncrypted):
		return fmt.Errorf("open the store: %w (set encryption.enabled: true and encryption.key)", err)
	case errors.Is(err, model.ErrInvalidEncryptionMarker):
		return fmt.Errorf("open the store: %w: an encrypted host was cut off marking the arcave project "+
			"(open it with encryption.enabled: true and its key)", err)
	default:
		return fmt.Errorf("open the arcave project store: %w", err)
	}
}

// closeQuietly closes c and logs any error without failing the caller.
func closeQuietly(ctx context.Context, name string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("close " + name)
	}
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

	tlsOpts, useTLS, err := newDockerTLSOptions(cfg)
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

// newDockerTLSOptions resolves the docker.* TLS fields into docker/cli's TLSOptions
// and reports whether any TLS material was configured (all-empty means "read the
// environment"). cert_file/key_file are a mutual-TLS pair, checked here because
// docker/cli would surface a half-set one only as an opaque handshake failure.
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

// newProgressWriter sets compose's global render mode and returns the writer compose renders on:
// stderr or nothing, or by default the adapter re-emitting its events as log records, returned
// again as what they reach a step through, nil otherwise.
func newProgressWriter(mode string, logger zerolog.Logger) (io.WriteCloser, *compose.ProgressLogger) {
	switch mode {
	case config.ProgressQuiet:
		// Discarding the stream too covers the warnings compose writes outside the UI.
		composeprogress.Mode = composeprogress.ModeQuiet
		return nopWriteCloser{io.Discard}, nil
	case config.ProgressTTY:
		// ModeAuto, not ModeTTY: compose falls back to plain lines in a pipe.
		composeprogress.Mode = composeprogress.ModeAuto
		return nopWriteCloser{os.Stderr}, nil
	default:
		composeprogress.Mode = composeprogress.ModeJSON
		adapter := compose.NewProgressLogger(logger)
		return adapter, adapter
	}
}

// composeEventSink puts compose's events on the progress step of the call, a resource per object.
type composeEventSink struct {
	step *progress.StepReporter
}

// newComposeEventSink is the sink of the step ctx carries; nil under a mute one, so the events
// stay in the log.
func newComposeEventSink(ctx context.Context) compose.EventSink {
	step := progress.Current(ctx)
	if step.IsMute() {
		return nil
	}
	return composeEventSink{step: step}
}

// Event reports one event as its object's line: in flight, settled, or a warning.
func (s composeEventSink) Event(evt compose.Event) {
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
	case compose.EventWorking:
		s.step.UpdateResource(resource, msg)
	case compose.EventDone:
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

// newDockerClient resolves cfg into the one docker/cli instance both halves of
// client/docker are built over, plus the APIClient it uses. progressOut becomes
// the CLI's error stream once the endpoint resolves; until then that stream is a
// buffer, being the only place docker/cli reports a failed resolution.
func newDockerClient(cfg config.Docker, progressOut io.Writer) (*command.DockerCli, client.APIClient, error) {
	clientOpts, err := newDockerClientOptions(cfg)
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

// newImageBackend builds the image backend over both archivers a writable layer may name;
// images are always deduplicated, which needs a daemon of Docker 25 or newer.
func newImageBackend(
	composeProjectStore storages.Navigator, origin model.LayerOrigin, engine *docker.Engine,
) (*imagecopy.Copier, error) {
	blobStore, err := composeProjectStore.SubStorage(dedupcopy.NamespaceBlobs, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at the shared blob store: %w", err)
	}
	dedup := dedupcopy.New(blobStore)
	return imagecopy.NewCopier(composeProjectStore, origin, engine, map[model.ImageBackendKind]imagecopy.ImageArchiver{
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
		SavepointCapture: capturepolicy.SavepointCaptureSettings{
			WritableLayers:   utils.Value(cfg.Capture.WritableLayers),
			Images:           utils.Value(cfg.Capture.Images),
			NamedVolumes:     utils.Value(cfg.Capture.Volumes.Named),
			BindVolumes:      utils.Value(cfg.Capture.Volumes.Bind),
			AnonymousVolumes: utils.Value(cfg.Capture.Volumes.Anonymous),
		},
	}
}

// gcDependencies is what gc runs on: the arcave project's store and the daemon; arcave mcp
// leaves its printer unused.
type gcDependencies struct {
	store   storages.Storager
	engine  *docker.Engine
	service *gc.GarbageCollector
	printer resultPrinter
}

// newGCDependencies wires a gc over the session's compose projects, one collector per kind. The
// daemon is only connected to here; whether it answers is the restored-layer collector's probe.
func newGCDependencies(
	ctx context.Context, projectPath string, cfg config.Config, sessionRef string,
) (*gcDependencies, error) {
	var opened partial
	defer func() { opened.releaseOnFailure(ctx) }()

	arcaveProject, err := openArcaveProjectStore(ctx, cfg, projectPath)
	if err != nil {
		return nil, err
	}
	root, arcaveProjectStore := arcaveProject.root, arcaveProject.store
	opened.add("store root", root)

	sessions := sessionrepo.New(arcaveProjectStore)
	session, err := newSessionService(projectPath, arcaveProjectStore, sessions).ResolveSession(ctx, sessionRef)
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	sessionLocker, err := newSessionLocker(arcaveProjectStore, session.ID)
	if err != nil {
		return nil, fmt.Errorf("open the session lock: %w", err)
	}
	// A docker.* the config gets wrong fails every command alike.
	_, dockerClientAPI, err := newDockerClient(cfg.Docker, io.Discard)
	if err != nil {
		return nil, err
	}
	engine := docker.NewEngine(dockerClientAPI)
	opened.add("docker engine", engine)

	service := gc.New(gc.Deps{
		Store:                    arcaveProjectStore,
		Session:                  session.ID,
		Sessions:                 sessions,
		NewSavepoints:            newGCSavepointReader,
		Locker:                   lock.New(projectPath),
		SessionLocker:            sessionLocker,
		Inflight:                 inflightrepo.New(projectPath),
		DaemonCollectors:         newDaemonCollectors(cfg, engine),
		ComposeProjectCollectors: newComposeProjectCollectors(cfg, engine, arcaveProject.resticPassword),
		DaemonErasers:            newDaemonErasers(engine),
		ComposeProjectErasers:    newComposeProjectErasers(cfg, engine, arcaveProject.resticPassword),
		// resticrepo reads the repositories through restic, not the listing.
		UnlistedNamespaces: []string{cdc.NamespaceRepos},
		RestoredLayerGrace: utils.Value(cfg.GC.RestoredLayerGrace),
	})
	opened.keep()
	return &gcDependencies{
		store:   root,
		engine:  engine,
		service: service,
		printer: loadRunOutput(),
	}, nil
}

func (d *gcDependencies) Close(ctx context.Context) error {
	closeQuietly(ctx, "docker engine", d.engine)
	return d.store.Close()
}

// newSavepointReader is the savepoint repository of a compose project's store, as a service
// reading across compose projects sees it.
func newSavepointReader(store storages.Storager) core.SavepointReader {
	return sprepo.New(store)
}

// newGCSavepointReader is the savepoint repository of a compose project's store, as gc reads it.
func newGCSavepointReader(store storages.Storager) gc.SavepointReader {
	return sprepo.New(store)
}

// newDaemonCollectors builds the collectors judging the daemon.
func newDaemonCollectors(cfg config.Config, engine *docker.Engine) []gc.DaemonCollector {
	return []gc.DaemonCollector{
		layertag.New(engine, model.ArcaveProjectName(cfg.Project.Name)),
	}
}

// newComposeProjectCollectors builds the collectors judging a compose project's store.
func newComposeProjectCollectors(
	cfg config.Config, engine *docker.Engine, resticPassword string,
) []gc.ComposeProjectCollector {
	return []gc.ComposeProjectCollector{
		savepointdir.New(),
		ociimage.New(),
		ociblob.New(cfg.Common.Concurrency),
		resticrepo.New(engine, resticInspectorOpener{
			cfg:            config.CDCWithDefaults(cfg.Backends.CDC),
			resticPassword: resticPassword,
			engine:         engine,
		}),
	}
}

// newDaemonErasers builds the erasers carrying out the gc's actions on the daemon.
func newDaemonErasers(engine *docker.Engine) []gc.DaemonEraser {
	return []gc.DaemonEraser{
		eraser.NewTagEraser(engine),
	}
}

// newComposeProjectErasers builds the erasers carrying out the gc's actions in a compose project's store.
func newComposeProjectErasers(
	cfg config.Config, engine *docker.Engine, resticPassword string,
) []gc.ComposeProjectEraser {
	return []gc.ComposeProjectEraser{
		eraser.NewTreeEraser(),
		eraser.NewImageEraser(),
		eraser.NewObjectEraser(),
		eraser.NewForgetEraser(resticPrunerOpener{
			cfg:            config.CDCWithDefaults(cfg.Backends.CDC),
			resticPassword: resticPassword,
			engine:         engine,
		}, cfg.Common.Concurrency),
	}
}

// openedRestic is one compose project's restic repositories, served to restic containers.
type openedRestic struct {
	// root is the repositories as objects, rooted at cdc.NamespaceRepos.
	root storages.Storager
	openedCDCTransport
}

// openResticRepositories opens the transport over store's repositories, as newDependencies
// serves them to the cdc backend; the transport's Close releases it.
func openResticRepositories(
	ctx context.Context, cfg config.CDCBackend, engine *docker.Engine, store storages.Navigator,
) (openedRestic, error) {
	resticRoot, err := store.SubStorage(cdc.NamespaceRepos, true)
	if err != nil {
		return openedRestic{}, fmt.Errorf("open restic repository root: %w", err)
	}
	transport, err := openCDCTransport(ctx, cfg, engine, resticRoot)
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
	cfg            config.CDCBackend
	resticPassword string
	engine         *docker.Engine
}

// Open opens the transport over store's repositories; the inspector's Close releases it.
func (o resticInspectorOpener) Open(ctx context.Context, store storages.Navigator) (resticrepo.Inspector, error) {
	served, err := openResticRepositories(ctx, o.cfg, o.engine, store)
	if err != nil {
		return nil, err
	}
	return resticInspector{
		Inspector:          cdc.NewInspector(o.engine, served.transport, newCDCOptions(o.cfg, o.resticPassword)),
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
	cfg            config.CDCBackend
	resticPassword string
	engine         *docker.Engine
}

// Open opens the transport over store's repositories; the pruner's Close releases it.
func (o resticPrunerOpener) Open(ctx context.Context, store storages.Navigator) (eraser.Pruner, error) {
	served, err := openResticRepositories(ctx, o.cfg, o.engine, store)
	if err != nil {
		return nil, err
	}
	return resticPruner{
		Pruner:             cdc.NewPruner(o.engine, served.root, served.transport, newCDCOptions(o.cfg, o.resticPassword)),
		openedCDCTransport: served.openedCDCTransport,
	}, nil
}

// resticPruner is a cdc.Pruner with the transport it prunes through.
type resticPruner struct {
	*cdc.Pruner
	openedCDCTransport
}
