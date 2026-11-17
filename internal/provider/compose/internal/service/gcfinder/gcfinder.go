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

// Package gcfinder wires the compose provider's gc: one collector per kind, one eraser per action.
package gcfinder

import (
	"context"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/layertag"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/ociblob"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/ociimage"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/resticrepo"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/savepointdir"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/eraser"
	"github.com/greenmaskio/arcave/internal/service/volumebackend/cdc"
)

// daemon is all a gc asks of the daemon.
type daemon interface {
	Ping(ctx context.Context) error
	ListLayerImages(ctx context.Context) ([]model.TaggedImage, error)
	ListContainerImages(ctx context.Context) ([]model.ContainerImage, error)
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// Deps are what a gc of the compose provider runs on.
type Deps struct {
	// NewSavepointReader opens the savepoints of a compose project's store.
	NewSavepointReader func(store storages.Storager) garbage.SavepointReader
	Daemon             daemon
	ArcaveProject      model.ArcaveProjectName
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency      int
	ResticInspectors resticrepo.InspectorOpener
	ResticPruners    eraser.PrunerOpener
}

// New builds a gc's finder, one collector per kind and one eraser per action.
func New(deps Deps) *garbage.Finder {
	return garbage.New(garbage.Deps{
		NewSavepointReader:       deps.NewSavepointReader,
		DaemonCollectors:         newDaemonCollectors(deps),
		ComposeProjectCollectors: newComposeProjectCollectors(deps),
		DaemonErasers:            newDaemonErasers(deps),
		ComposeProjectErasers:    newComposeProjectErasers(deps),
		// resticrepo reads the repositories through restic, not the listing.
		UnlistedNamespaces: []string{cdc.NamespaceRepos},
	})
}

// newDaemonCollectors builds the collectors judging the daemon.
func newDaemonCollectors(deps Deps) []garbage.DaemonCollector {
	return []garbage.DaemonCollector{
		layertag.New(deps.Daemon, deps.ArcaveProject),
	}
}

// newComposeProjectCollectors builds the collectors judging a compose project's store.
func newComposeProjectCollectors(deps Deps) []garbage.ComposeProjectCollector {
	return []garbage.ComposeProjectCollector{
		savepointdir.New(),
		ociimage.New(),
		ociblob.New(deps.Concurrency),
		resticrepo.New(deps.Daemon, deps.ResticInspectors),
	}
}

// newDaemonErasers builds the erasers carrying out the gc's actions on the daemon.
func newDaemonErasers(deps Deps) []garbage.DaemonEraser {
	return []garbage.DaemonEraser{
		eraser.NewTagEraser(deps.Daemon),
	}
}

// newComposeProjectErasers builds the erasers carrying out the gc's actions in a compose project's store.
func newComposeProjectErasers(deps Deps) []garbage.ComposeProjectEraser {
	return []garbage.ComposeProjectEraser{
		eraser.NewTreeEraser(),
		eraser.NewImageEraser(),
		eraser.NewObjectEraser(),
		eraser.NewForgetEraser(deps.ResticPruners, deps.Concurrency),
	}
}
