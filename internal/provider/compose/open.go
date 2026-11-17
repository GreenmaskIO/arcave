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
	"io"
	"slices"

	dockercompose "github.com/docker/compose/v5/pkg/compose"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/workload"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc"
)

// Config is what the compose provider is opened with.
type Config struct {
	// Settings are the provider's own sections of arcave's config, defaults applied.
	Settings composeconfig.Config
	// ArcaveProject is project.name: the namespace of the layer tags a rollback leaves.
	ArcaveProject model.ArcaveProjectName
	// Concurrency is common.concurrency: how many units one fan-out works at once; 0 leaves it
	// unbounded.
	Concurrency int
	// ComposeProgress is where compose's own progress goes: compose.progress as the DI root resolved
	// it against its own output.
	ComposeProgress string
}

// Workload is an opened workload of one compose project, with what it holds open.
type Workload struct {
	provider.Workload
	opened closers
}

// Close releases what Open opened, newest first.
func (w Workload) Close(ctx context.Context) { w.opened.release(ctx) }

// ComposeContainers reads one compose project's containers off the daemon, with what it holds open.
type ComposeContainers struct {
	*introspect.Inspector
	opened closers
}

// Close releases the connection OpenComposeContainers opened.
func (c ComposeContainers) Close(ctx context.Context) { c.opened.release(ctx) }

// Open connects the daemon and opens the workload of composeProject over its store; on error it
// releases whatever it had opened.
func Open(
	ctx context.Context, cfg Config, store storages.Storager, composeProject model.DockerComposeProjectName,
) (Workload, error) {
	var opened closers
	workload, err := openWorkload(ctx, cfg, store, composeProject, &opened)
	if err != nil {
		opened.release(ctx)
		return Workload{}, err
	}
	return Workload{
		Workload: workload,
		opened:   opened,
	}, nil
}

// OpenComposeContainers connects the daemon and reads composeProject's containers off it. Nothing
// here runs compose, so the CLI's progress channel goes nowhere; no runtime profile: the container
// list and the compat inspect read alike on every engine.
func OpenComposeContainers(cfg Config, composeProject model.DockerComposeProjectName) (ComposeContainers, error) {
	var opened closers
	daemon, err := connectDaemon(cfg.Settings.Docker, io.Discard, &opened)
	if err != nil {
		return ComposeContainers{}, err
	}
	return ComposeContainers{
		Inspector: introspect.NewInspector(daemon.api, composeProject),
		opened:    opened,
	}, nil
}

// openWorkload wires the workload of composeProject over its store, the daemon connected; what it
// opens goes on opened.
func openWorkload(
	ctx context.Context, cfg Config, store storages.Storager,
	composeProject model.DockerComposeProjectName, opened *closers,
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
	cdcTransport, err := openCDCTransport(ctx, composeconfig.CDCWithDefaults(settings.Backends.CDC), profile.engine, resticRoot)
	if err != nil {
		return nil, err
	}
	opened.addFunc(cdcTransport.Close)

	backendRegistry, err := newBackendRegistry(settings, resticRoot, profile.volumes, profile.engine, cdcTransport.transport)
	if err != nil {
		return nil, err
	}
	origin := model.LayerOrigin{
		ArcaveProject:  cfg.ArcaveProject,
		ComposeProject: composeProject,
	}
	imageBackend, err := newImageBackend(store, origin, profile.engine)
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
		CaptureDefaults:    newCaptureDefaults(settings),
		LeftoverVolumes:    settings.Compose.LeftoverVolumesPolicy(),
		DaemonVersion:      profile.version,
		MinDaemonMajor:     profile.minDaemonMajor,
		Store:              store,
		Concurrency:        cfg.Concurrency,
	}), nil
}

// closers is what an Open has opened, released newest first.
type closers struct {
	releases []func(ctx context.Context)
}

// add puts closer on the stack; a failure to close it is logged under name.
func (c *closers) add(name string, closer io.Closer) {
	c.addFunc(func(ctx context.Context) { closeQuietly(ctx, name, closer) })
}

// addFunc puts a release that reports its own failure on the stack.
func (c *closers) addFunc(fn func(ctx context.Context)) {
	c.releases = append(c.releases, fn)
}

// release runs the stack newest first.
func (c *closers) release(ctx context.Context) {
	for _, v := range slices.Backward(c.releases) {
		v(ctx)
	}
}

// closeQuietly closes closer and logs any error without failing the caller.
func closeQuietly(ctx context.Context, name string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("close " + name)
	}
}
