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
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"io"

	dockercompose "github.com/docker/compose/v5/pkg/compose"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composeclient "github.com/greenmaskio/arcave/internal/provider/compose/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/client/docker"
	sprepo "github.com/greenmaskio/arcave/internal/provider/compose/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/inflightmarker"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/volumebackend/cdc"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/workload"
	"github.com/greenmaskio/arcave/internal/utils"
)

// Config is what the compose provider is opened with.
type Config struct {
	// Settings are the provider's own sections of arcave's config, defaults applied.
	Settings composeconfig.Config
	// ArcaveProject is project.name: the namespace of the layer tags a rollback leaves and of the
	// inflight marker.
	ArcaveProject model.ArcaveProjectName
	// Concurrency is common.concurrency: how many units one fan-out works at once; below 1 leaves
	// the capture and rollback fan-outs unbounded.
	Concurrency int
	// ComposeProgress is where compose's own progress goes: compose.progress as the DI root resolved
	// it against its own output.
	ComposeProgress string
}

// Workload is an opened workload of one compose project, with what it holds open.
type Workload struct {
	provider.Workload
	opened utils.Closers
}

// Close releases what Open opened, newest first.
func (w Workload) Close(ctx context.Context) { w.opened.Release(ctx) }

// Observer reads one compose project off the daemon for a status, with what it holds open.
type Observer struct {
	provider.Observer
	opened utils.Closers
}

// Close releases the connection OpenObserver opened.
func (o Observer) Close(ctx context.Context) { o.opened.Release(ctx) }

// Surveyor reads and drops one compose project for a down, with what it holds open.
type Surveyor struct {
	provider.Surveyor
	opened utils.Closers
}

// Close releases what OpenSurveyor opened.
func (s Surveyor) Close(ctx context.Context) { s.opened.Release(ctx) }

// InflightMarkers is the marker of an interrupted rollback of the arcave project, kept on the
// daemon it guards, with the connection it holds open.
type InflightMarkers struct {
	*inflightmarker.Store
	opened utils.Closers
}

// Close releases the connection OpenInflightMarkers opened.
func (m InflightMarkers) Close(ctx context.Context) { m.opened.Release(ctx) }

// Open connects the daemon and opens the workload of composeProject over its store; on error it
// releases whatever it had opened.
func Open(
	ctx context.Context, cfg Config, store storages.Storager, composeProject model.DockerComposeProjectName,
) (Workload, error) {
	var opened utils.Closers
	workload, err := openWorkload(ctx, cfg, store, composeProject, &opened)
	if err != nil {
		opened.Release(ctx)
		return Workload{}, err
	}
	return Workload{
		Workload: workload,
		opened:   opened,
	}, nil
}

// OpenObserver connects the daemon to observe composeProject on it. Nothing here runs compose, so
// the CLI's progress channel goes nowhere; no runtime profile: the container list and the compat
// inspect read alike on every engine.
func OpenObserver(cfg Config, composeProject model.DockerComposeProjectName) (Observer, error) {
	var opened utils.Closers
	daemon, err := connectDaemon(cfg.Settings.Docker, io.Discard, &opened)
	if err != nil {
		return Observer{}, err
	}
	return Observer{
		Observer: introspect.NewReader(daemon.api, composeProject),
		opened:   opened,
	}, nil
}

// OpenInflightMarkers connects the daemon to keep cfg.ArcaveProject's inflight marker on it, as
// composeProject writes and clears it; a volume's labels need no runtime profile.
func OpenInflightMarkers(cfg Config, composeProject model.DockerComposeProjectName) (InflightMarkers, error) {
	var opened utils.Closers
	daemon, err := connectDaemon(cfg.Settings.Docker, io.Discard, &opened)
	if err != nil {
		return InflightMarkers{}, err
	}
	return InflightMarkers{
		Store:  inflightmarker.NewStore(daemon.api, cfg.ArcaveProject, composeProject),
		opened: opened,
	}, nil
}

// OpenSurveyor connects the daemon to survey and drop composeProject on it, with none of what a
// capture or a restore needs: no store, no backend, no cdc transport. No runtime profile either:
// the listings, compose's down, a volume's removal and an untag read alike on every engine.
func OpenSurveyor(ctx context.Context, cfg Config, composeProject model.DockerComposeProjectName) (Surveyor, error) {
	var opened utils.Closers
	surveyor, err := openSurveyor(ctx, cfg, composeProject, &opened)
	if err != nil {
		opened.Release(ctx)
		return Surveyor{}, err
	}
	return Surveyor{
		Surveyor: surveyor,
		opened:   opened,
	}, nil
}

// openSurveyor wires the workload of composeProject with what a drop needs alone, the daemon
// connected; what it opens goes on opened.
func openSurveyor(
	ctx context.Context, cfg Config, composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (provider.Surveyor, error) {
	composeOutput := newComposeProgress(cfg.ComposeProgress, *log.Ctx(ctx))
	daemon, err := connectDaemon(cfg.Settings.Docker, composeOutput.stream, opened)
	if err != nil {
		return nil, err
	}
	composeAPI, err := dockercompose.NewComposeService(daemon.cli,
		dockercompose.WithEventProcessor(composeOutput.processor),
	)
	if err != nil {
		return nil, fmt.Errorf("initialise compose: %w", err)
	}
	engine := docker.NewEngine(daemon.api)
	return workload.New(workload.Deps{
		ComposeReader: introspect.NewReader(daemon.api, composeProject),
		ComposeRunner: composeclient.NewClient(composeAPI, composeclient.Options{
			WaitTimeout:     cfg.Settings.Compose.WaitTimeout,
			StopTimeout:     cfg.Settings.Compose.StopTimeout,
			Events:          composeOutput.events,
			SinkFromContext: newComposeEventSink,
		}),
		VolumeRemover:  engine,
		ProjectRemover: engine,
		LayerTags:      engine,
		LayerOrigin: composemodel.LayerOrigin{
			ArcaveProject:  cfg.ArcaveProject,
			ComposeProject: composeProject,
		},
	}), nil
}

// openWorkload wires the workload of composeProject over its store, the daemon connected; what it
// opens goes on opened.
func openWorkload(
	ctx context.Context, cfg Config, store storages.Storager,
	composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (provider.Workload, error) {
	settings := cfg.Settings
	// Where compose's own progress goes, on the logger initRoot resolved.
	composeOutput := newComposeProgress(cfg.ComposeProgress, *log.Ctx(ctx))
	daemon, err := connectDaemon(settings.Docker, composeOutput.stream, opened)
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
	cdcTransport, err := openCDCTransport(ctx, composeconfig.CDCWithDefaults(settings.Backends.CDC), profile.Engine, resticRoot)
	if err != nil {
		return nil, err
	}
	opened.AddFunc(cdcTransport.Close)

	backendRegistry, err := newBackendRegistry(settings, resticRoot, profile.Volumes, profile.Engine, cdcTransport.transport)
	if err != nil {
		return nil, err
	}
	origin := composemodel.LayerOrigin{
		ArcaveProject:  cfg.ArcaveProject,
		ComposeProject: composeProject,
	}
	imageBackend, err := newImageBackend(store, origin, profile.Engine)
	if err != nil {
		return nil, err
	}

	// One runner for the capture and the rollback: it routes compose's events to the call's step.
	composeRunner := composeclient.NewClient(composeAPI, composeclient.Options{
		WaitTimeout:     settings.Compose.WaitTimeout,
		StopTimeout:     settings.Compose.StopTimeout,
		Events:          composeOutput.events,
		Prompter:        prompter,
		SinkFromContext: newComposeEventSink,
	})
	return workload.New(workload.Deps{
		// One store for the records and the data, so every record travels with its data.
		SavepointRepo:    sprepo.New(store),
		ComposeReader:    introspect.NewReader(profile.API, composeProject),
		ImageBackend:     imageBackend,
		ComposeRunner:    composeRunner,
		ContainerControl: profile.Engine,
		VolumeRemover:    profile.Engine,
		ProjectRemover:   profile.Engine,
		// The per-replica half of a layer restore: what compose's one image per
		// service cannot express, the engine rebuilds container by container.
		ContainerRebuilder: profile.Engine,
		ImageLookup:        profile.Engine,
		ProjectAssembler:   profile.Assembler,
		SpecCodec:          composespec.NewCodec(),
		BackendRegistry:    backendRegistry,
		CaptureDefaults:    newCaptureDefaults(settings),
		LeftoverVolumes:    settings.Compose.LeftoverVolumesPolicy(),
		DaemonVersion:      profile.Version,
		MinDaemonMajor:     profile.MinDaemonMajor,
		Store:              store,
		LayerTags:          profile.Engine,
		LayerOrigin:        origin,
		Concurrency:        cfg.Concurrency,
	}), nil
}
