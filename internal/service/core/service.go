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

package core

import (
	"cmp"
	"context"
	"time"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
)

type projectLocker interface {
	Acquire(ctx context.Context) (release func() error, err error)
}

// writeAdmission admits a workflow's writes to the arcave project's store, under the project lock
// and before the first: an encrypted one is marked for its key then.
type writeAdmission interface {
	AdmitWrite(ctx context.Context) error
}

// sessionLookup finds a session's record; core re-checks it under the lock.
type sessionLookup interface {
	LoadSession(ctx context.Context, id model.SessionID) (model.Session, bool, error)
}

// inflightMarkers is the intent marker's seam: written before a rollback's first
// destructive write, cleared when the environment is consistent again.
type inflightMarkers interface {
	Get(ctx context.Context) (marker model.InflightMarker, found bool, err error)
	Put(ctx context.Context, marker model.InflightMarker) error
	Clear(ctx context.Context) error
}

// Deps are Service's collaborators. A struct rather than a parameter list: the
// seams have no natural order, so a new one could silently swap with an existing one.
type Deps struct {
	// Savepoints are the compose project's savepoints, as the provider keeps them.
	Savepoints    provider.Savepoints
	ProjectLocker projectLocker
	// WriteAdmission admits a workflow's writes to the arcave project's store, before the first.
	WriteAdmission writeAdmission
	// SessionLocker holds Session's lock in the store for the whole of a workflow.
	SessionLocker model.SessionLocker
	// Workload is the compose project the workflows act on, through the provider's stages.
	Workload provider.Workload
	Inflight inflightMarkers
	// ArcaveProject is the arcave project the workload belongs to, which a rollback's deployment
	// labels name.
	ArcaveProject model.ArcaveProjectName
	// Session is the session the workload belongs to; zero means model.DefaultSession.
	Session model.SessionID
	// ComposeProject is the compose project the workload is.
	ComposeProject model.DockerComposeProjectName
	// Sessions checks, under the lock, that Session still exists.
	Sessions sessionLookup
	// Store is the compose project's store, a savepoint's tree and its heartbeat below it.
	// TODO: goes with the savepoint's tree moving behind a provider stage; see startSavepointHeartbeat.
	Store storages.Navigator
	// RunnerInfo identifies this process in a savepoint's heartbeat.
	RunnerInfo heartbeat.RunnerInfo
	// HeartbeatInterval and HeartbeatStaleAfter time a savepoint's heartbeat; zero keeps
	// heartbeat.DefaultInterval and heartbeat.DefaultStaleAfter.
	HeartbeatInterval   time.Duration
	HeartbeatStaleAfter time.Duration
}

// Service orchestrates arcave's savepoint workflows over one workload; a capture and a rollback
// read savepoints through the embedded SavepointStoreService.
type Service struct {
	*SavepointStoreService
	workload            provider.Workload
	arcaveProject       model.ArcaveProjectName
	store               storages.Navigator
	runnerInfo          heartbeat.RunnerInfo
	heartbeatInterval   time.Duration
	heartbeatStaleAfter time.Duration
}

func New(deps Deps) *Service {
	return &Service{
		SavepointStoreService: NewSavepointStoreService(SavepointStoreDeps{
			Savepoints:     deps.Savepoints,
			ProjectLocker:  deps.ProjectLocker,
			WriteAdmission: deps.WriteAdmission,
			SessionLocker:  deps.SessionLocker,
			Inflight:       deps.Inflight,
			Session:        deps.Session,
			ComposeProject: deps.ComposeProject,
			Sessions:       deps.Sessions,
		}),
		workload:            deps.Workload,
		arcaveProject:       deps.ArcaveProject,
		store:               deps.Store,
		runnerInfo:          deps.RunnerInfo,
		heartbeatInterval:   cmp.Or(deps.HeartbeatInterval, heartbeat.DefaultInterval),
		heartbeatStaleAfter: cmp.Or(deps.HeartbeatStaleAfter, heartbeat.DefaultStaleAfter),
	}
}
