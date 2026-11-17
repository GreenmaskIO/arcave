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

package sessionstore

import (
	"cmp"
	"context"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// garbageFinder finds the garbage of session in the arcave project's store.
type garbageFinder interface {
	Find(
		ctx context.Context, store storages.Navigator, session model.SessionID, dataKinds []model.GarbageDataKind,
	) []provider.Garbage
}

// Deps are a session's collaborators.
type Deps struct {
	// Store is the arcave project's store the session lies in.
	Store storages.Navigator
	// Session is the session; zero is model.DefaultSession.
	Session model.SessionID
	// NewSavepointRecords opens the savepoints of a compose project's store.
	NewSavepointRecords func(store storages.Storager) SavepointRecords
	// Garbage finds the session's garbage; nil finds none.
	Garbage garbageFinder
}

// session is one session's compose projects, reached with no daemon but what Garbage reads.
type session struct {
	store               storages.Navigator
	id                  model.SessionID
	newSavepointRecords func(store storages.Storager) SavepointRecords
	garbage             garbageFinder
}

// New is the session deps names. It opens nothing.
func New(deps Deps) provider.Session {
	return session{
		store:               deps.Store,
		id:                  cmp.Or(deps.Session, model.DefaultSession),
		newSavepointRecords: deps.NewSavepointRecords,
		garbage:             deps.Garbage,
	}
}

// ID is the session's id.
func (s session) ID() model.SessionID {
	return s.id
}

// Workloads names the compose projects the session holds, sorted.
func (s session) Workloads(ctx context.Context) ([]provider.WorkloadName, error) {
	sessionStore, err := storefactory.Session(s.store, s.id)
	if err != nil {
		return nil, err
	}
	names, err := storefactory.ComposeProjects(ctx, sessionStore)
	if err != nil {
		return nil, err
	}
	workloads := make([]provider.WorkloadName, 0, len(names))
	for _, name := range names {
		workloads = append(workloads, provider.WorkloadName(name))
	}
	return workloads, nil
}

// Savepoints opens the savepoints of the compose project workload names.
func (s session) Savepoints(workload provider.WorkloadName) (provider.Savepoints, error) {
	sessionStore, err := storefactory.Session(s.store, s.id)
	if err != nil {
		return nil, err
	}
	store, err := storefactory.ComposeProject(sessionStore, model.DockerComposeProjectName(workload))
	if err != nil {
		return nil, err
	}
	return NewSavepoints(s.newSavepointRecords(store)), nil
}

// FindGarbage finds what the session's committed savepoints and running containers do not hold.
func (s session) FindGarbage(ctx context.Context, dataKinds []model.GarbageDataKind) []provider.Garbage {
	if s.garbage == nil {
		return nil
	}
	return s.garbage.Find(ctx, s.store, s.id, dataKinds)
}
