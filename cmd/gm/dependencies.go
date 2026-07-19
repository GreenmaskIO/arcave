package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/client/docker"
	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/lock"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/printer/json"
	cfgrepo "github.com/greenmaskio/gm-tools/internal/repository/config"
	sprepo "github.com/greenmaskio/gm-tools/internal/repository/savepoint"
	"github.com/greenmaskio/gm-tools/internal/service/core"
	"github.com/greenmaskio/gm-tools/internal/service/snapshot"
	"github.com/greenmaskio/gm-tools/internal/storages"
)

// safetyBackupDir is the store safety backups live in, relative to the project
// root. Unlike the savepoint store it is never user-configurable: it is scratch
// space a rollback creates and deletes within one command.
var safetyBackupDir = filepath.Join(".gm", "tmp")

// dependencies is the composition root's bag of per-command collaborators. It
// names the concrete types it constructs: an interface here would buy no
// polymorphism, and hiding *docker.Engine behind one only cost Close a type
// assertion.
type dependencies struct {
	savepointStore    model.Storager
	safetyBackupStore model.Storager
	engine            *docker.Engine
	service           *core.Service
	printer           *json.Printer
}

// initAllDependencies wires the savepoint workflows for the project rooted at
// projectPath. Each store is built by storages.New, which resolves and validates
// the configured path once and hands back a Storager already rooted at it — so
// nothing below this line ever names a directory.
func initAllDependencies(ctx context.Context, projectPath string, cfg config.Config) (*dependencies, error) {
	savepointStore, err := storages.New(cfg.Storage, projectPath)
	if err != nil {
		return nil, fmt.Errorf("initialise savepoint store: %w", err)
	}

	// The safety-backup store is not user-configurable, so it is built from a
	// literal directory arm rather than from cfg. It goes through the same
	// factory, and therefore the same root validation, as the savepoint store.
	safetyBackupStore, err := storages.New(config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: safetyBackupDir},
	}, projectPath)
	if err != nil {
		closeQuietly(ctx, "savepoint store", savepointStore)
		return nil, fmt.Errorf("initialise safety backup store: %w", err)
	}

	// The Docker engine is created once here and injected into the snapshot
	// backends — and, since it owns the resolved daemon connection, into the
	// compose runner too, so both halves of client/docker target one daemon.
	// NewEngine only resolves the options; the connection itself is built on the
	// first call that needs it, so commands that never touch a mount (savepoint
	// list) create it without contacting the daemon. Every closer is owned by the
	// returned dependencies and released by its Close — never deferred here, or
	// they would shut down before the command ran.
	dockerOpts := docker.Options{
		Host:       cfg.Docker.Host,
		APIVersion: cfg.Docker.APIVersion,
		Context:    cfg.Docker.Context,
		CertPath:   cfg.Docker.CertPath,
		TLSVerify:  cfg.Docker.TLSVerify,
	}
	engine, err := docker.NewEngine(dockerOpts)
	if err != nil {
		closeQuietly(ctx, "savepoint store", savepointStore)
		closeQuietly(ctx, "safety backup store", safetyBackupStore)
		return nil, fmt.Errorf("initialise docker engine: %w", err)
	}

	// The savepoint repository shares the savepoint store with the backend that
	// writes the archives it records, so a record always travels with its data —
	// move the store to S3 and the records go with it. The safety-backup store
	// gets no records: a backup is addressed by its namespace alone.
	// The locker is bound to the project root here, exactly as each backend is
	// bound to its store, so the service serialises its own mutating workflows
	// without ever naming a path.
	svc := core.New(
		sprepo.New(savepointStore),
		lock.New(projectPath),
		compose.NewInspector(),
		docker.NewComposeRunner(engine),
		snapshot.NewCopyBackend(engine, savepointStore),
		snapshot.NewCopyBackend(engine, safetyBackupStore),
	)
	return &dependencies{
		savepointStore:    savepointStore,
		safetyBackupStore: safetyBackupStore,
		engine:            engine,
		service:           svc,
		printer:           json.New(os.Stdout),
	}, nil
}

// Close releases the Docker engine and both stores. Only the savepoint store's
// error is returned: it is the one holding the command's data, and a failure to
// close the others cannot change the command's outcome.
func (d *dependencies) Close(ctx context.Context) error {
	closeQuietly(ctx, "docker engine", d.engine)
	closeQuietly(ctx, "safety backup store", d.safetyBackupStore)
	return d.savepointStore.Close()
}

// initServiceDeps wires `gm init`, which needs neither storage, nor a snapshot
// backend, nor the Docker engine — only somewhere to write the config and a
// compose project to verify. Nothing here holds a resource, so there is nothing
// to close.
func initServiceDeps(projectPath string) (*core.InitService, *json.Printer) {
	svc := core.NewInitService(projectPath, cfgrepo.New(projectPath), compose.NewInspector())
	return svc, json.New(os.Stdout)
}

// closeQuietly closes c and logs any error without failing the caller — used for
// teardown of a resource whose close error is secondary to the work just done.
func closeQuietly(ctx context.Context, name string, c io.Closer) {
	if err := c.Close(); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("close " + name)
	}
}
