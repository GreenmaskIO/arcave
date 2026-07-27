package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/api"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/tlsconfig"
	"github.com/greenmaskio/arcave/internal/client/compose"
	introspect "github.com/greenmaskio/arcave/internal/service/introspectv2"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	arcrepo "github.com/greenmaskio/arcave/internal/repository/arc"
	cfgrepo "github.com/greenmaskio/arcave/internal/repository/config"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/service/printer/json"
	"github.com/greenmaskio/arcave/internal/service/snapshot"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// safetyBackupDir is the store safety backups live in, relative to the project
// root. Unlike the savepoint store it is never user-configurable: it is scratch
// space a rollback creates and deletes within one command.
var safetyBackupDir = filepath.Join(model.StateDir, "tmp")

// dependencies is the composition root's bag of per-command collaborators. It
// names the concrete types it constructs: an interface here would buy no
// polymorphism, and hiding *docker.Engine behind one only cost Close a type
// assertion.
type dependencies struct {
	savepointStore    storages.Storager
	safetyBackupStore storages.Storager
	progressWriter    io.WriteCloser
	dockerCli         *command.DockerCli
	dockerClient      client.APIClient
	composeService    api.Compose
	engine            *docker.Engine
	composeRunner     *compose.Client
	service           *core.Service
	printer           *json.Printer
}

// initAllDependencies wires the savepoint workflows for the project rooted at
// projectPath. Each store is built by storefactory.New, which resolves the
// configured path once, roots it under the static arcave namespace and the
// session, and hands back a Storager already rooted there — so nothing below this
// line ever names a directory.
func initAllDependencies(ctx context.Context, projectPath string, cfg config.Config) (*dependencies, error) {
	savepointStore, err := storefactory.New(cfg.Storage, projectPath, model.DefaultSession)
	if err != nil {
		return nil, fmt.Errorf("initialise savepoint store: %w", err)
	}

	// The safety-backup store is not user-configurable, so it is built from a
	// literal directory arm rather than from cfg. It goes through the same
	// factory, and therefore the same namespacing, as the savepoint store.
	safetyBackupStore, err := storefactory.New(config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: safetyBackupDir},
	}, projectPath, model.DefaultSession)
	if err != nil {
		closeQuietly(ctx, "savepoint store", savepointStore)
		return nil, fmt.Errorf("initialise safety backup store: %w", err)
	}

	// Where compose's own progress goes. The logger comes from the context — the
	// one initRoot resolved — because the adapter is handed a logger rather than
	// reaching for zerolog's global: it is a client-layer type, and the
	// composition root is where the resolved logger lives.
	progressWriter := composeProgressWriter(cfg.Compose.Progress, *log.Ctx(ctx))

	// The docker/cli instance is resolved once here and is the single daemon
	// connection both halves of client/docker are built over: the engine takes its
	// APIClient, the compose service is constructed on the instance itself. That
	// shared construction is what makes "both halves target one daemon" a property
	// rather than a contract each has to keep by hand. Every closer is owned by the
	// returned dependencies and released by its Close — never deferred here, or
	// they would shut down before the command ran.
	dockerCli, dockerClientAPI, err := initDockerClient(cfg.Docker, progressWriter)
	if err != nil {
		closeQuietly(ctx, "savepoint store", savepointStore)
		closeQuietly(ctx, "safety backup store", safetyBackupStore)
		closeQuietly(ctx, "compose progress writer", progressWriter)
		return nil, fmt.Errorf("initialise docker client: %w", err)
	}
	engine := docker.NewEngine(dockerClientAPI)

	composeService := dockercompose.NewComposeService(dockerCli)
	dockerComposeLoader := compose.NewLoader()
	composeRunner := compose.NewClient(composeService, dockerComposeLoader, compose.Options{
		WaitTimeout: cfg.Compose.WaitTimeout,
		StopTimeout: cfg.Compose.StopTimeout,
	})

	// The savepoint repository shares the savepoint store with the backend that
	// writes the archives it records, so a record always travels with its data —
	// move the store to S3 and the records go with it. The safety-backup store
	// gets no records: a backup is addressed by its namespace alone.
	// The locker is bound to the project root here, exactly as each backend is
	// bound to its store, so the service serialises its own mutating workflows
	// without ever naming a path. Both take the session store: a savepoint's
	// records and its archives are what dies with the session.
	svc := core.New(
		sprepo.New(savepointStore),
		lock.New(projectPath),
		introspect.NewInspector(dockerComposeLoader),
		composeRunner,
		snapshot.NewCopyBackend(engine, savepointStore),
		snapshot.NewCopyBackend(engine, safetyBackupStore),
	)
	return &dependencies{
		savepointStore:    savepointStore,
		safetyBackupStore: safetyBackupStore,
		progressWriter:    progressWriter,
		dockerCli:         dockerCli,
		dockerClient:      dockerClientAPI,
		composeService:    composeService,
		engine:            engine,
		composeRunner:     composeRunner,
		service:           svc,
		printer:           json.New(os.Stdout),
	}, nil
}

// Close releases the Docker engine, the progress writer and both stores. Only
// the savepoint store's error is returned: it is the one holding the command's
// data, and a failure to close the others cannot change the command's outcome.
//
// The progress writer is closed after the engine, so a last line compose left
// unterminated is still emitted — and closed at all because that trailing line
// would otherwise sit in its buffer forever.
//
// dockerClient is not closed here: it is the connection the engine was built
// over, so closing the engine already releases it.
func (d *dependencies) Close(ctx context.Context) error {
	closeQuietly(ctx, "docker engine", d.engine)
	closeQuietly(ctx, "compose progress writer", d.progressWriter)
	closeQuietly(ctx, "safety backup store", d.safetyBackupStore)
	return d.savepointStore.Close()
}

// initServiceDeps wires `arcave init`, which needs neither storage, nor a snapshot
// backend, nor the Docker engine — only somewhere to write the config and a
// compose project to verify. Nothing here holds a resource, so there is nothing
// to close.
func initServiceDeps(projectPath string) (*core.InitService, *json.Printer) {
	svc := core.NewInitService(projectPath, cfgrepo.New(projectPath), introspect.NewInspector(compose.NewLoader()))
	return svc, json.New(os.Stdout)
}

// recommendServiceDeps wires `arcave recommend`, which is strictly read-only: it
// parses the compose files and the recommender catalog and reaches for nothing
// else — no storage, no snapshot backend, no Docker engine, no lock.
// recommenderFiles are the optional user catalogs merged over the built-in one,
// in order; empty when the caller passed none. Nothing here holds a resource, so
// there is nothing to close.
func recommendServiceDeps(recommenderFiles []string) (*core.RecommendService, *json.Printer) {
	return newRecommendService(recommenderFiles), json.New(os.Stdout)
}

// newRecommendService builds the recommend service alone, without a printer:
// `arcave mcp` needs one per tool call (the catalogs arrive with the call, and the
// service is bound to its catalog at construction) and must not print — its stdout
// is the JSON-RPC channel.
func newRecommendService(recommenderFiles []string) *core.RecommendService {
	return core.NewRecommendService(arcrepo.NewARC(recommenderFiles...), introspect.NewInspector(compose.NewLoader()))
}

// closeQuietly closes c and logs any error without failing the caller — used for
// teardown of a resource whose close error is secondary to the work just done.
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
// and reports whether any TLS material was configured at all (an all-empty config
// means "read the environment", so TLS stays off). It keeps docker's own two-part
// model rather than treating the fields as one block: a client certificate is a
// cert_file/key_file *pair* (mutual TLS) that must be supplied together, while
// ca_file/tls_verify govern server verification independently — tls_verify alone
// is valid, verifying the daemon against the system roots with no client cert.
// cert_path is the DOCKER_CERT_PATH shorthand that supplies ca.pem/cert.pem/key.pem
// from one directory, so it satisfies the pair on its own.
//
// The pair is validated here, at the composition root, because a half-set pair is a
// footgun docker/cli would surface only as an opaque handshake failure at connect
// time — the error names the field the user must add.
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

	if caFile == "" && certFile == "" && keyFile == "" && !cfg.TLSVerify {
		return nil, false, nil
	}

	return &tlsconfig.Options{
		CAFile:   caFile,
		CertFile: certFile,
		KeyFile:  keyFile,
	}, true, nil
}

// tlsFile resolves one TLS file path, mirroring the docker CLI's own layering: an
// explicit path wins; otherwise it is CertPath/name (the DOCKER_CERT_PATH
// convention); and when neither is set the field stays empty so docker/cli reads
// it from the environment rather than being handed a bogus path.
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
// safe to hand to a Close that closes whatever writer it was given. Closing
// os.Stderr would take the process's error stream down with it.
//
// It is spelled out here because the standard library has no writer counterpart:
// io.NopCloser wraps a Reader, and io.NopWriteCloser has been proposed
// (golang/go#22823) and never accepted. The name mirrors what that function
// would be called, so replacing this is mechanical if it ever lands.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// composeProgressWriter translates the project's compose.progress setting into
// the library's global render mode plus the writer compose renders on. Whatever
// it returns, the destination is **stderr or nothing**: arcave's output contract
// is JSON on stdout, and compose draws its per-container progress on the error
// stream (`stdinfo()`).
//
// The default is ProgressLog: compose is put in its JSON event mode and the
// stream goes to compose.ProgressLogger, which re-emits each event as an arcave
// log record. That is what makes the run speak one format — compose's renderer
// wrote raw text regardless of log.format, which under `log.format: json`
// corrupted the very stream an agent or a CI job parses.
//
// ProgressTTY hands the terminal back to compose's own renderer. It selects
// ModeAuto rather than ModeTTY on purpose: compose then redraws on a terminal
// and falls back to plain lines in a pipe, where forcing ModeTTY would spray
// ANSI escapes into a log file.
//
// progress.Mode is a package-level variable of the compose library. Setting a
// global belongs here if anywhere: the composition root is the one place that
// knows the whole process's configuration, and arcave runs one command per process.
func composeProgressWriter(mode string, logger zerolog.Logger) io.WriteCloser {
	switch mode {
	case config.ProgressQuiet:
		// The mode alone suppresses the progress UI; discarding the stream as well
		// covers the warnings compose writes there outside it.
		progress.Mode = progress.ModeQuiet
		return nopWriteCloser{io.Discard}
	case config.ProgressTTY:
		progress.Mode = progress.ModeAuto
		return nopWriteCloser{os.Stderr}
	default:
		progress.Mode = progress.ModeJSON
		return compose.NewProgressLogger(logger)
	}
}

// initDockerClient resolves cfg into the one docker/cli instance both halves of
// client/docker are built over, and returns the APIClient it will use alongside
// it — built here rather than taken from dockerCli.Client(), which reports a
// failure by calling os.Exit(1).
//
// progressOut is where the CLI's error stream — compose's progress channel —
// points once the endpoint has resolved. Until then it is a buffer, because that
// stream is the only place docker/cli reports a failed resolution.
func initDockerClient(cfg config.Docker, progressOut io.Writer) (*command.DockerCli, client.APIClient, error) {
	clientOpts, err := getDockerOptions(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// DOCKER_API_VERSION is the only seam docker/cli leaves for pinning the API
	// version: the client it builds from the resolved endpoint reads the version
	// from the environment (client.WithVersionFromEnv) and otherwise negotiates.
	// Setting it here is safe in a way it would not have been for the exec-based
	// version — arcave spawns no child processes, so this configures arcave's own client
	// and nothing else.
	if cfg.APIVersion != "" {
		if err := os.Setenv(client.EnvOverrideAPIVersion, cfg.APIVersion); err != nil {
			return nil, nil, fmt.Errorf("pin docker api version %q: %w", cfg.APIVersion, err)
		}
	}

	// stdout is discarded — compose draws its progress UI there and arcave's own
	// output contract is JSON on stdout. stderr is a buffer *for now*: it is the
	// only channel docker/cli reports a failed endpoint resolution on, and the
	// buffer is what turns that report into a returned error below.
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

	// The connection is built here rather than left to dockerCli.Client(), which
	// resolves lazily and answers a failure with os.Exit(1). NewAPIClientFromFlags
	// runs the same resolution — the same context store, the same endpoint, the
	// same client options, so DOCKER_API_VERSION is still honoured — and returns
	// an error instead of killing the process. Injecting the result back means the
	// CLI (and therefore the compose library built over it) uses this very client
	// rather than constructing a second one behind the exit.
	apiClient, err := command.NewAPIClientFromFlags(clientOpts, dockerCli.ConfigFile())
	if err != nil {
		return nil, nil, fmt.Errorf("connect to docker engine (docker.host %q, docker.context %q): %w", cfg.Host, cfg.Context, err)
	}
	if err := command.WithAPIClient(apiClient)(dockerCli); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}

	// Initialize can log a benign warning of its own (a malformed
	// ~/.docker/config.json), so the buffer is cleared here: from this point on
	// anything in it comes from the CLI's own resolution, and nothing else.
	initErr.Reset()

	// One resolution is still the CLI's alone: even with the client injected, its
	// sync.Once opens with getDockerEndPoint, and a failure there is reported only
	// by printing to the error stream — after which Client() calls os.Exit(1).
	// Force that Once here, through DockerEndpoint rather than Client(): both run
	// it, but DockerEndpoint prints the cause and returns. Forcing it *here* is
	// what matters, since the compose library calls Client() itself, well past any
	// error handling of arcave's.
	dockerCli.DockerEndpoint()
	if initErr.Len() > 0 {
		return nil, nil, fmt.Errorf("connect to docker engine (docker.host %q, docker.context %q): %s",
			cfg.Host, cfg.Context, strings.TrimSpace(initErr.String()))
	}

	// Resolution succeeded, so the error stream becomes compose's progress
	// channel for the rest of the run — nothing must accumulate in the buffer.
	if err := command.WithErrorStream(progressOut)(dockerCli); err != nil {
		return nil, nil, fmt.Errorf("initialise docker client: %w", err)
	}
	return dockerCli, apiClient, nil
}
