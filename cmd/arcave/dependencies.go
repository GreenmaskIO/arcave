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
	"errors"
	"fmt"
	"os"

	"filippo.io/age"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/repository/currentsession"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
	sessionrepo "github.com/greenmaskio/arcave/internal/repository/session"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/core/gc"
	"github.com/greenmaskio/arcave/internal/service/core/session"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/service/sessionlock"
	"github.com/greenmaskio/arcave/internal/service/status"
	"github.com/greenmaskio/arcave/internal/service/storekey"
	"github.com/greenmaskio/arcave/internal/storefactory"
	"github.com/greenmaskio/arcave/internal/utils"
)

// sessionScope is the arcave project's store and the session a command works in.
type sessionScope struct {
	arcaveProjectStore storages.Storager
	writeAdmission     writeAdmission
	sessions           *sessionrepo.Repository
	sessionID          model.SessionID
	sessionLocker      *sessionlock.Locker
}

// openSessionScope opens the arcave project's store and resolves sessionRef on it.
func openSessionScope(
	ctx context.Context, cfg config.Config, projectPath, sessionRef string, opened *utils.Closers,
) (sessionScope, error) {
	arcaveProject, err := openArcaveProjectStore(ctx, cfg, projectPath, opened)
	if err != nil {
		return sessionScope{}, err
	}
	sessions := sessionrepo.New(arcaveProject.store)
	session, err := newSessionService(projectPath, arcaveProject, sessions).ResolveSession(ctx, sessionRef)
	if err != nil {
		return sessionScope{}, fmt.Errorf("resolve session: %w", err)
	}
	sessionLocker, err := newSessionLocker(arcaveProject.store, session.ID)
	if err != nil {
		return sessionScope{}, fmt.Errorf("open the session lock: %w", err)
	}
	return sessionScope{
		arcaveProjectStore: arcaveProject.store,
		writeAdmission:     arcaveProject.writeAdmission,
		sessions:           sessions,
		sessionID:          session.ID,
		sessionLocker:      sessionLocker,
	}, nil
}

// composeProjectScope is one compose project's store inside its session.
type composeProjectScope struct {
	sessionScope
	composeProject model.DockerComposeProjectName
	// store holds savepoint records, fullcopy captures and a cdc repository root alike, rooted
	// inside the session that they die with.
	store storages.Storager
}

// openComposeProjectScope descends from the session to composeProject's store.
func openComposeProjectScope(
	ctx context.Context, cfg config.Config, projectPath, sessionRef string,
	composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (composeProjectScope, error) {
	scope, err := openSessionScope(ctx, cfg, projectPath, sessionRef, opened)
	if err != nil {
		return composeProjectScope{}, err
	}
	sessionStore, err := storefactory.Session(scope.arcaveProjectStore, scope.sessionID)
	if err != nil {
		return composeProjectScope{}, fmt.Errorf("initialise session store: %w", err)
	}
	store, err := storefactory.ComposeProject(sessionStore, composeProject)
	if err != nil {
		return composeProjectScope{}, fmt.Errorf("initialise compose project store: %w", err)
	}
	return composeProjectScope{
		sessionScope:   scope,
		composeProject: composeProject,
		store:          store,
	}, nil
}

// openSavepointService wires every savepoint workflow for composeProject in the session sessionRef
// resolves to, the daemon connected; what it opens goes on opened.
func openSavepointService(
	ctx context.Context, projectPath string, cfg config.Config, sessionRef string,
	composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (*core.Service, error) {
	scope, err := openComposeProjectScope(ctx, cfg, projectPath, sessionRef, composeProject, opened)
	if err != nil {
		return nil, err
	}
	workload, err := openComposeWorkload(ctx, cfg, scope.store, composeProject, opened)
	if err != nil {
		return nil, err
	}
	inflightMarkers, err := openComposeInflightMarkers(cfg, composeProject, opened)
	if err != nil {
		return nil, err
	}

	// The locker is bound to the project root here, so the service never names a path.
	return core.New(core.Deps{
		Savepoints:     newComposeSavepoints(scope.store),
		ProjectLocker:  lock.New(projectPath),
		WriteAdmission: scope.writeAdmission,
		// The session's lock in the store, which the flock beside it cannot be across hosts.
		SessionLocker:  scope.sessionLocker,
		Workload:       workload,
		Inflight:       inflightMarkers,
		ArcaveProject:  model.ArcaveProjectName(cfg.Project.Name),
		Session:        scope.sessionID,
		ComposeProject: composeProject,
		// Re-checked under the lock: a session deleted since --session was resolved.
		Sessions:   scope.sessions,
		Store:      scope.store,
		RunnerInfo: newRunnerInfo(),
	}), nil
}

// openDownService wires down for composeProject in the session sessionRef resolves to: the daemon,
// the marker and the flock, and of the store the session's record alone. Nothing a capture or a
// restore needs is built, and no session lock or write admission: a down writes nothing to the
// store.
func openDownService(
	ctx context.Context, projectPath string, cfg config.Config, sessionRef string,
	composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (*core.DownService, error) {
	if err := storefactory.ValidateComposeProjectName(composeProject); err != nil {
		return nil, err
	}
	scope, err := openSessionScope(ctx, cfg, projectPath, sessionRef, opened)
	if err != nil {
		return nil, err
	}
	surveyor, err := openComposeSurveyor(ctx, cfg, composeProject, opened)
	if err != nil {
		return nil, err
	}
	inflightMarkers, err := openComposeInflightMarkers(cfg, composeProject, opened)
	if err != nil {
		return nil, err
	}
	return core.NewDownService(core.DownDeps{
		ProjectLocker:  lock.New(projectPath),
		Inflight:       inflightMarkers,
		Sessions:       scope.sessions,
		Session:        scope.sessionID,
		ArcaveProject:  model.ArcaveProjectName(cfg.Project.Name),
		ComposeProject: composeProject,
		Workload:       surveyor,
	}), nil
}

// openSavepointStoreService wires the savepoint workflows that read and delete in the store alone:
// nothing that reaches the daemon is built.
func openSavepointStoreService(
	ctx context.Context, projectPath string, cfg config.Config, sessionRef string,
	composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (*core.SavepointStoreService, error) {
	scope, err := openComposeProjectScope(ctx, cfg, projectPath, sessionRef, composeProject, opened)
	if err != nil {
		return nil, err
	}
	return core.NewSavepointStoreService(core.SavepointStoreDeps{
		Savepoints:     newComposeSavepoints(scope.store),
		ProjectLocker:  lock.New(projectPath),
		WriteAdmission: scope.writeAdmission,
		SessionLocker:  scope.sessionLocker,
		Session:        scope.sessionID,
		ComposeProject: composeProject,
		Sessions:       scope.sessions,
	}), nil
}

// openStatusService wires status for composeProject; the store opens lazily, so one that fails
// only makes the status partial.
func openStatusService(
	projectPath string, cfg config.Config, composeProject model.DockerComposeProjectName, opened *utils.Closers,
) (*status.Service, error) {
	if err := storefactory.ValidateComposeProjectName(composeProject); err != nil {
		return nil, err
	}
	observer, err := openComposeObserver(cfg, composeProject, opened)
	if err != nil {
		return nil, err
	}
	inflightMarkers, err := openComposeInflightMarkers(cfg, composeProject, opened)
	if err != nil {
		return nil, err
	}
	return status.New(status.Deps{
		Observer: observer,
		Inflight: inflightMarkers,
		Current:  currentsession.New(projectPath),
		OpenArcaveProjectStore: func(ctx context.Context) (storages.Storager, error) {
			arcaveProject, err := openArcaveProjectStore(ctx, cfg, projectPath, opened)
			if err != nil {
				return nil, err
			}
			return arcaveProject.store, nil
		},
		NewSessionReader:   newSessionReader,
		NewProviderSession: newComposeSession,
		ArcaveProject:      model.ArcaveProjectName(cfg.Project.Name),
		ComposeProject:     composeProject,
	}), nil
}

// openSessionService wires the session commands: the arcave project's store and nothing that
// reaches the daemon.
func openSessionService(
	ctx context.Context, projectPath string, cfg config.Config, opened *utils.Closers,
) (*session.Service, error) {
	arcaveProject, err := openArcaveProjectStore(ctx, cfg, projectPath, opened)
	if err != nil {
		return nil, err
	}
	return newSessionService(projectPath, arcaveProject, sessionrepo.New(arcaveProject.store)), nil
}

// newSessionService wires the session commands over the arcave project's store.
func newSessionService(
	projectPath string, arcaveProject openedArcaveProject, sessions *sessionrepo.Repository,
) *session.Service {
	return session.New(session.Deps{
		Sessions:       sessions,
		Locker:         lock.New(projectPath),
		WriteAdmission: arcaveProject.writeAdmission,
		Current:        currentsession.New(projectPath),
		Store:          arcaveProject.store,
		NewSession: func(id model.SessionID) provider.Session {
			return newComposeSession(arcaveProject.store, id)
		},
		NewSessionLocker: func(id model.SessionID) (model.SessionLocker, error) {
			return newSessionLocker(arcaveProject.store, id)
		},
		RunnerInfo: newRunnerInfo(),
	})
}

// newSessionLocker opens the lock of session id in the arcave project's store.
func newSessionLocker(arcaveProjectStore storages.Navigator, id model.SessionID) (*sessionlock.Locker, error) {
	store, key, err := storefactory.SessionLock(arcaveProjectStore, id)
	if err != nil {
		return nil, err
	}
	return sessionlock.New(sessionlock.NewFischerSessionLockStore(store, key), id, newRunnerInfo()), nil
}

// newRunnerInfo identifies this process in a heartbeat; an unknown host stays blank.
func newRunnerInfo() heartbeat.RunnerInfo {
	host, err := os.Hostname()
	if err != nil {
		log.Debug().Err(err).Msg("reading the hostname for the heartbeat")
	}
	return heartbeat.RunnerInfo{Host: host, PID: os.Getpid(), ArcaveVersion: version}
}

// writeAdmission admits a workflow's writes to the arcave project's store: storekey's
// PlainAdmission or EncryptedAdmission, as the encryption section opened it.
type writeAdmission interface {
	AdmitWrite(ctx context.Context) error
}

// openedArcaveProject is the arcave project's store and what admits a workflow's writes to it.
type openedArcaveProject struct {
	store          storages.Storager
	writeAdmission writeAdmission
}

// openArcaveProjectStore opens the store root onto opened and descends to the arcave project,
// encrypted under encryption.enabled. It writes nothing; writeAdmission marks the project.
func openArcaveProjectStore(
	ctx context.Context, cfg config.Config, projectPath string, opened *utils.Closers,
) (openedArcaveProject, error) {
	root, err := storefactory.Root(ctx, cfg.Storage, projectPath)
	if err != nil {
		return openedArcaveProject{}, fmt.Errorf("initialise store root: %w", err)
	}
	opened.Add("store root", root)
	return openArcaveProject(ctx, cfg, root)
}

func openArcaveProject(ctx context.Context, cfg config.Config, root storages.Storager) (openedArcaveProject, error) {
	var (
		plain    storages.Storager
		store    storages.Storager
		identity *age.X25519Identity
		err      error
	)
	if plain, err = storefactory.ArcaveProject(root, model.ArcaveProjectName(cfg.Project.Name)); err != nil {
		return openedArcaveProject{}, fmt.Errorf("initialise arcave project store: %w", err)
	}
	markers := encryptionmarker.New(plain)
	if !cfg.Encryption.Enabled {
		if err = storekey.RequirePlain(ctx, markers); err != nil {
			return openedArcaveProject{}, newStoreKeyError(err)
		}
		return openedArcaveProject{
			store:          plain,
			writeAdmission: fixedAdmission{admission: storekey.NewPlainAdmission(markers)},
		}, nil
	}
	if identity, err = storekey.ParseKey(cfg.Encryption.Key); err != nil {
		return openedArcaveProject{}, newStoreKeyError(err)
	}
	contents := arcaveProjectContents{store: plain}
	if err = storekey.RequireEncrypted(ctx, markers, contents, identity); err != nil {
		return openedArcaveProject{}, newStoreKeyError(err)
	}
	if store, err = storefactory.Encrypt(plain, identity); err != nil {
		return openedArcaveProject{}, err
	}
	return openedArcaveProject{
		store:          store,
		writeAdmission: fixedAdmission{admission: storekey.NewEncryptedAdmission(markers, contents, identity)},
	}, nil
}

// fixedAdmission adds the fix to a write refused under the lock, as an open's refusal carries it.
type fixedAdmission struct {
	admission writeAdmission
}

func (a fixedAdmission) AdmitWrite(ctx context.Context) error {
	err := a.admission.AdmitWrite(ctx)
	if fix, ok := storeKeyFix(err); ok {
		return fmt.Errorf("%w (%s)", err, fix)
	}
	return err
}

// arcaveProjectContents is the plain arcave project store's listing, for storekey.
type arcaveProjectContents struct {
	store storages.Navigator
}

// HoldsData reports whether the arcave project holds anything but its marker.
func (c arcaveProjectContents) HoldsData(ctx context.Context) (bool, error) {
	return storefactory.ArcaveProjectHoldsData(ctx, c.store)
}

// newStoreKeyError adds the fix to a store the encryption section does not open.
func newStoreKeyError(err error) error {
	if fix, ok := storeKeyFix(err); ok {
		return fmt.Errorf("open the store: %w (%s)", err, fix)
	}
	return fmt.Errorf("open the arcave project store: %w", err)
}

// storeKeyFix is what to do about a store the encryption section refuses; false for any other error.
func storeKeyFix(err error) (string, bool) {
	switch {
	case errors.Is(err, model.ErrInvalidStoreKey), errors.Is(err, model.ErrWrongStoreKey):
		return "check encryption.key, " + encryptionKeyEnv + " by default", true
	case errors.Is(err, model.ErrStoreEncrypted):
		return "set encryption.enabled: true and encryption.key", true
	case errors.Is(err, model.ErrStoreHoldsPlainData):
		return "turn encryption.enabled off to use it, or start an encrypted one under another project.name — " +
			"see \"Turning encryption on for a project with data\" in troubleshooting", true
	default:
		return "", false
	}
}

// openGCService wires a gc over the session's compose projects, one collector per kind. The daemon
// is only connected to here.
func openGCService(
	ctx context.Context, projectPath string, cfg config.Config, sessionRef string, opened *utils.Closers,
) (*gc.GarbageCollector, error) {
	scope, err := openSessionScope(ctx, cfg, projectPath, sessionRef, opened)
	if err != nil {
		return nil, err
	}
	providerSession, err := openComposeGarbageSession(cfg, scope.arcaveProjectStore, scope.sessionID, opened)
	if err != nil {
		return nil, err
	}
	// gc spans the session's compose projects and only reads the marker: no compose project writes it.
	inflightMarkers, err := openComposeInflightMarkers(cfg, "", opened)
	if err != nil {
		return nil, err
	}
	return gc.New(gc.Deps{
		Sessions:        scope.sessions,
		ProviderSession: providerSession,
		Locker:          lock.New(projectPath),
		WriteAdmission:  scope.writeAdmission,
		SessionLocker:   scope.sessionLocker,
		Inflight:        inflightMarkers,
		RetainRecent:    utils.Value(cfg.GC.SafetySnapshots.RetainRecent),
	}), nil
}

// newSessionReader is the session repository of an arcave project's store, as a reader.
func newSessionReader(store storages.Storager) status.SessionReader {
	return sessionrepo.New(store)
}
