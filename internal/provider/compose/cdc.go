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

package compose

import (
	"context"
	"fmt"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/resticrepo"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/eraser"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc/rest"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc/rest/repo"
)

// resticShutdownTimeout bounds the restic server's graceful shutdown.
const resticShutdownTimeout = 5 * time.Second

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
	ctx context.Context, cfg composeconfig.CDCBackend, daemon hostLoopbackReader, resticRoot storages.Storager,
) (openedCDCTransport, error) {
	switch cfg.Transport {
	case composeconfig.CDCTransportStdio:
		return openedCDCTransport{
			transport: cdc.StdioTransport{
				Server: rest.NewConnServer(ctx, repo.NewResolver(resticRoot)),
			},
		}, nil
	case composeconfig.CDCTransportTCP:
		return openTCPTransport(ctx, cfg, daemon, resticRoot)
	default:
		return openedCDCTransport{}, fmt.Errorf("unsupported backends.cdc.transport %q", cfg.Transport)
	}
}

// openTCPTransport starts the server restic containers dial, and names the URL base
// they dial it at.
func openTCPTransport(
	ctx context.Context, cfg composeconfig.CDCBackend, daemon hostLoopbackReader, resticRoot storages.Storager,
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
	ctx context.Context, cfg composeconfig.CDCBackend, daemon hostLoopbackReader,
) (hostNetwork bool, containerHost string, err error) {
	switch cfg.NetworkMode {
	case composeconfig.NetworkModeHost:
		return true, "127.0.0.1", nil
	case composeconfig.NetworkModeBridge:
		return false, cfg.ContainerHost, nil
	default: // composeconfig.NetworkModeAuto — Validate admits nothing else.
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
func newCDCOptions(cfg composeconfig.CDCBackend) cdc.Options {
	return cdc.Options{
		Image:       cfg.Image,
		Incremental: cfg.RestoreMode == composeconfig.RestoreModeIncremental,
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

// resticDaemon is what a gc's restic openers ask of the daemon. It is the client itself, with no
// runtime profile: a call added here must depend on nothing podman.API changes.
type resticDaemon interface {
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

// openResticRepositories opens the transport over store's repositories, as Open serves them to the
// cdc backend; the transport's Close releases it.
func openResticRepositories(
	ctx context.Context, cfg composeconfig.CDCBackend, daemon hostLoopbackReader, store storages.Navigator,
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
	cfg    composeconfig.CDCBackend
	daemon resticDaemon
}

// Open opens the transport over store's repositories; the inspector's Close releases it.
func (o resticInspectorOpener) Open(ctx context.Context, store storages.Navigator) (resticrepo.Inspector, error) {
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
	cfg    composeconfig.CDCBackend
	daemon resticDaemon
}

// Open opens the transport over store's repositories; the pruner's Close releases it.
func (o resticPrunerOpener) Open(ctx context.Context, store storages.Navigator) (eraser.Pruner, error) {
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
