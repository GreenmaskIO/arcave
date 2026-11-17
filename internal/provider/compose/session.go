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
	"io"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/client/docker"
	sprepo "github.com/greenmaskio/arcave/internal/provider/compose/internal/repository/savepoint"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/garbage"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/gcfinder"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/sessionstore"
)

// Session is an opened session of the compose provider, with what it holds open.
type Session struct {
	provider.Session
	opened closers
}

// Close releases what OpenSession opened.
func (s Session) Close(ctx context.Context) { s.opened.release(ctx) }

// garbageFinder finds a session's garbage.
type garbageFinder interface {
	Find(
		ctx context.Context, store storages.Navigator, session model.SessionID, dataKinds []model.GarbageDataKind,
	) []provider.Garbage
}

// NewStoreSavepoints is the savepoints in a compose project's store, reached with no daemon.
func NewStoreSavepoints(store storages.Storager) provider.Savepoints {
	return sessionstore.NewSavepoints(sprepo.New(store))
}

// NewStoreSession is session id in the arcave project's store, reached with no daemon.
func NewStoreSession(arcaveProjectStore storages.Navigator, id model.SessionID) provider.Session {
	return newSession(arcaveProjectStore, id, nil)
}

// OpenSession connects the daemon and opens session id with what a gc finds in it. Nothing a gc
// reads differs by engine, so it runs over the client itself, with no runtime profile; whether the
// daemon answers is the restored-layer collector's probe.
func OpenSession(cfg Config, arcaveProjectStore storages.Navigator, id model.SessionID) (Session, error) {
	var opened closers
	// A docker.* the config gets wrong fails every command alike.
	daemon, err := connectDaemon(cfg.Settings.Docker, io.Discard, &opened)
	if err != nil {
		return Session{}, err
	}
	engine := docker.NewEngine(daemon.api)
	cdcConfig := composeconfig.CDCWithDefaults(cfg.Settings.Backends.CDC)
	finder := gcfinder.New(gcfinder.Deps{
		NewSavepointReader: func(store storages.Storager) garbage.SavepointReader {
			return sprepo.New(store)
		},
		Daemon:        engine,
		ArcaveProject: cfg.ArcaveProject,
		Concurrency:   cfg.Concurrency,
		ResticInspectors: resticInspectorOpener{
			cfg:    cdcConfig,
			daemon: engine,
		},
		ResticPruners: resticPrunerOpener{
			cfg:    cdcConfig,
			daemon: engine,
		},
	})
	return Session{
		Session: newSession(arcaveProjectStore, id, finder),
		opened:  opened,
	}, nil
}

// newSession is session id in the arcave project's store, its garbage found by finder; a nil finder
// finds none. It opens nothing.
func newSession(arcaveProjectStore storages.Navigator, id model.SessionID, finder garbageFinder) provider.Session {
	return sessionstore.New(sessionstore.Deps{
		Store:   arcaveProjectStore,
		Session: id,
		NewSavepointRecords: func(store storages.Storager) sessionstore.SavepointRecords {
			return sprepo.New(store)
		},
		Garbage: finder,
	})
}
