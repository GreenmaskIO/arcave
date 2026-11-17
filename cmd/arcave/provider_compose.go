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
	"context"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/service/status"
)

// openComposeWorkload opens the compose provider's workload of composeProject over its store, the
// daemon connected; what it opens goes on opened.
func openComposeWorkload(
	ctx context.Context, cfg config.Config, store storages.Storager,
	composeProject model.DockerComposeProjectName, opened *closers,
) (provider.Workload, error) {
	// Where compose's own progress goes, on the logger initRoot resolved.
	composeProgress := composeProgressMode(
		ctx, cfg.ComposeProvider.Compose.Progress, runProgressMode == progressModeTTY,
	)
	workload, err := compose.Open(ctx, newComposeConfig(cfg, composeProgress), store, composeProject)
	if err != nil {
		return nil, err
	}
	opened.addFunc(workload.Close)
	return workload, nil
}

// newComposeSavepoints is the compose provider's savepoint records in a compose project's store.
func newComposeSavepoints(store storages.Storager) provider.Savepoints {
	return compose.NewStoreSavepoints(store)
}

// newComposeSession is the compose provider's side of session id, reached with no daemon.
func newComposeSession(arcaveProjectStore storages.Storager, id model.SessionID) provider.Session {
	return compose.NewStoreSession(arcaveProjectStore, id)
}

// openComposeGarbageSession is the compose provider's side of session id with its garbage, the
// daemon connected; what it opens goes on opened.
func openComposeGarbageSession(
	cfg config.Config, arcaveProjectStore storages.Storager, id model.SessionID, opened *closers,
) (provider.Session, error) {
	// Nothing a gc runs reports compose's progress.
	session, err := compose.OpenSession(newComposeConfig(cfg, ""), arcaveProjectStore, id)
	if err != nil {
		return nil, err
	}
	opened.addFunc(session.Close)
	return session, nil
}

// openComposeContainers reads composeProject's containers off the daemon, as status does; what it
// opens goes on opened.
func openComposeContainers(
	cfg config.Config, composeProject model.DockerComposeProjectName, opened *closers,
) (compose.ComposeContainers, error) {
	containers, err := compose.OpenComposeContainers(newComposeConfig(cfg, ""), composeProject)
	if err != nil {
		return compose.ComposeContainers{}, err
	}
	opened.addFunc(containers.Close)
	return containers, nil
}

// newComposeConfig is what the compose provider is opened with: its own sections of cfg and the
// two arcave settings it reads, project.name and common.concurrency.
func newComposeConfig(cfg config.Config, composeProgress string) compose.Config {
	return compose.Config{
		Settings:        cfg.ComposeProvider,
		ArcaveProject:   model.ArcaveProjectName(cfg.Project.Name),
		Concurrency:     cfg.Common.Concurrency,
		ComposeProgress: composeProgress,
	}
}

// composeProgressMode keeps compose's own renderer off a terminal arcave's tree is drawn on:
// two renderers on one stderr tear each other's frames.
func composeProgressMode(ctx context.Context, mode string, drawing bool) string {
	if !drawing || mode != composeconfig.ProgressTTY {
		return mode
	}
	log.Ctx(ctx).Warn().Msg("compose.progress: tty is ignored while arcave draws its own progress; " +
		"compose's progress goes to the log, pass --progress log to keep compose's renderer")
	return composeconfig.ProgressLog
}

// newSavepointLoader is the savepoint repository of a compose project's store, as status reads it.
func newSavepointLoader(store storages.Storager) status.SavepointLoader {
	return sprepo.New(store)
}
