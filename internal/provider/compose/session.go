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

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/backendregistry"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/layertag"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/ociblob"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/ociimage"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/resticrepo"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/collector/savepointdir"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage/eraser"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
)

// ResticInspectorOpener serves a compose project's restic repositories to a gc reading them, and
// ResticPrunerOpener to one pruning them; the DI root opens the transport.
type (
	ResticInspectorOpener = resticrepo.InspectorOpener
	ResticInspector       = resticrepo.Inspector
	ResticPrunerOpener    = eraser.PrunerOpener
	ResticPruner          = eraser.Pruner
)

// SavepointRepository is the records of one compose project's savepoints, as the provider reads
// and removes them with no daemon.
type SavepointRepository interface {
	ListSavepoints(ctx context.Context) ([]model.Savepoint, error)
	LoadSavepoints(ctx context.Context, ids []model.SavepointID) ([]model.Savepoint, error)
	// DeleteSavepoint uncommits the record, then deletes the tree.
	DeleteSavepoint(ctx context.Context, id model.SavepointID) error
}

// garbageFinder finds a session's garbage.
type garbageFinder interface {
	Find(
		ctx context.Context, store storages.Navigator, session model.SessionID, dataKinds []model.GarbageDataKind,
	) []provider.Garbage
}

// gcDaemon is all a gc asks of the daemon.
type gcDaemon interface {
	Ping(ctx context.Context) error
	ListLayerImages(ctx context.Context) ([]model.TaggedImage, error)
	ListContainerImages(ctx context.Context) ([]model.ContainerImage, error)
	RemoveImageRef(ctx context.Context, ref model.ImageRef) error
}

// SessionDeps are what one session of the compose provider is opened with.
type SessionDeps struct {
	// Store is the arcave project's store the session lies in.
	Store storages.Navigator
	// Session is the session; zero is model.DefaultSession.
	Session model.SessionID
	// NewSavepointRepository opens the savepoints of a compose project's store.
	NewSavepointRepository func(store storages.Storager) SavepointRepository
	// Garbage is what a gc finds and deletes the session's garbage with; nil finds none.
	Garbage *GarbageDeps
}

// GarbageDeps are what a gc of the compose provider runs on.
type GarbageDeps struct {
	Daemon        gcDaemon
	ArcaveProject model.ArcaveProjectName
	// Concurrency caps how many units one fan-out works at once; 0 leaves it unbounded.
	Concurrency      int
	ResticInspectors ResticInspectorOpener
	ResticPruners    ResticPrunerOpener
}

// NewSavepoints is one compose project's savepoints over records, reached with no daemon.
func NewSavepoints(records SavepointRepository) provider.Savepoints {
	return sessionstore.NewSavepoints(records)
}

// NewSession is one session of the compose provider: its compose projects, their savepoints, and,
// with deps.Garbage, what a gc finds in it. It opens nothing.
func NewSession(deps SessionDeps) provider.Session {
	// An interface, not *garbage.Finder: a nil pointer in it would not be a nil finder.
	var finder garbageFinder
	if deps.Garbage != nil {
		finder = newGarbageFinder(deps.NewSavepointRepository, *deps.Garbage)
	}
	return sessionstore.New(sessionstore.Deps{
		Store:   deps.Store,
		Session: deps.Session,
		NewSavepointRecords: func(store storages.Storager) sessionstore.SavepointRecords {
			return deps.NewSavepointRepository(store)
		},
		Garbage: finder,
	})
}

// NewBackendRegistry indexes the volume backends' factories by kind; a duplicate is an error.
func NewBackendRegistry(factories ...model.SnapshotBackendFactory) (model.SnapshotBackendRegistry, error) {
	return backendregistry.New(factories...)
}

// newGarbageFinder builds a gc's finder, one collector per kind and one eraser per action.
func newGarbageFinder(
	newSavepointRepository func(store storages.Storager) SavepointRepository, deps GarbageDeps,
) *garbage.Finder {
	return garbage.New(garbage.Deps{
		NewSavepointReader: func(store storages.Storager) garbage.SavepointReader {
			return newSavepointRepository(store)
		},
		DaemonCollectors:         newDaemonCollectors(deps),
		ComposeProjectCollectors: newComposeProjectCollectors(deps),
		DaemonErasers:            newDaemonErasers(deps),
		ComposeProjectErasers:    newComposeProjectErasers(deps),
		// resticrepo reads the repositories through restic, not the listing.
		UnlistedNamespaces: []string{cdc.NamespaceRepos},
	})
}

// newDaemonCollectors builds the collectors judging the daemon.
func newDaemonCollectors(deps GarbageDeps) []garbage.DaemonCollector {
	return []garbage.DaemonCollector{
		layertag.New(deps.Daemon, deps.ArcaveProject),
	}
}

// newComposeProjectCollectors builds the collectors judging a compose project's store.
func newComposeProjectCollectors(deps GarbageDeps) []garbage.ComposeProjectCollector {
	return []garbage.ComposeProjectCollector{
		savepointdir.New(),
		ociimage.New(),
		ociblob.New(deps.Concurrency),
		resticrepo.New(deps.Daemon, deps.ResticInspectors),
	}
}

// newDaemonErasers builds the erasers carrying out the gc's actions on the daemon.
func newDaemonErasers(deps GarbageDeps) []garbage.DaemonEraser {
	return []garbage.DaemonEraser{
		eraser.NewTagEraser(deps.Daemon),
	}
}

// newComposeProjectErasers builds the erasers carrying out the gc's actions in a compose project's store.
func newComposeProjectErasers(deps GarbageDeps) []garbage.ComposeProjectEraser {
	return []garbage.ComposeProjectEraser{
		eraser.NewTreeEraser(),
		eraser.NewImageEraser(),
		eraser.NewObjectEraser(),
		eraser.NewForgetEraser(deps.ResticPruners, deps.Concurrency),
	}
}
