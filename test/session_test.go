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

package test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/currentsession"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sprepo "github.com/greenmaskio/arcave/internal/repository/savepoint"
	sessionrepo "github.com/greenmaskio/arcave/internal/repository/session"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy/dedupcopy"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/service/introspect"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// sessionArcaveProject is the arcave project every session test stores under.
const sessionArcaveProject = "arcave-session-tenant"

// sessionEnv is one arcave project directory, its sessions managed the way cmd/arcave
// wires the session commands: the real store, lock and inflight marker.
type sessionEnv struct {
	ctx         context.Context
	projectRoot string
	storage     config.Storage
	sessions    *core.SessionService
	inflight    *inflightrepo.Repository
	daemon      *client.Client
	project     string
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()

	projectRoot := t.TempDir()
	storage := config.Storage{
		Type:      config.StorageTypeDirectory,
		Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
	}
	root, err := storefactory.Root(storage, projectRoot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	arcaveProjectStore, err := storefactory.ArcaveProject(root, sessionArcaveProject)
	require.NoError(t, err)

	inflight := inflightrepo.New(projectRoot)
	return &sessionEnv{
		ctx:         zerolog.New(io.Discard).WithContext(context.Background()),
		projectRoot: projectRoot,
		storage:     storage,
		sessions: core.NewSessionService(core.SessionDeps{
			Sessions: sessionrepo.New(arcaveProjectStore),
			Locker:   lock.New(projectRoot),
			Inflight: inflight,
			Current:  currentsession.New(projectRoot),
			Store:    arcaveProjectStore,
		}),
		inflight: inflight,
	}
}

// storeDir is the store root on disk, every arcave project beneath it.
func (e *sessionEnv) storeDir() string {
	return filepath.Join(e.projectRoot, e.storage.Directory.Path, "arcave")
}

// sessionDir is where a session's tree lives on disk.
func (e *sessionEnv) sessionDir(id model.SessionID) string {
	return filepath.Join(e.storeDir(), "projects", sessionArcaveProject, "sessions", id.String())
}

// create makes a session named name and fails the test if it cannot.
func (e *sessionEnv) create(t *testing.T, name string) model.Session {
	t.Helper()

	record, err := e.sessions.CreateSession(e.ctx, model.CreateSessionRequest{Name: name})
	require.NoError(t, err)
	return record
}

// names are the listed sessions' names, in listing order.
func (e *sessionEnv) names(t *testing.T) []string {
	t.Helper()

	summaries, err := e.sessions.ListSessions(e.ctx, model.ListSessionsRequest{})
	require.NoError(t, err)
	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	return names
}

// marker writes the inflight marker an interrupted rollback in session would leave.
func (e *sessionEnv) marker(t *testing.T, session model.SessionID) {
	t.Helper()

	require.NoError(t, e.inflight.Put(e.ctx, model.InflightMarker{
		Phase:          model.InflightRestoring,
		Session:        session,
		ComposeProject: "arcave-session-it",
		Target:         model.NewSavepointID(),
		Safety:         model.NewSavepointID(),
		At:             time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
	}))
}

func TestSession_CreateListResolveDelete(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	require.Equal(t, []string{model.DefaultSessionName}, env.names(t), "a fresh store lists the default alone")
	a := env.create(t, "feat-a")
	b := env.create(t, "feat-b")
	unnamed := env.create(t, "")

	// Act
	summaries, err := env.sessions.ListSessions(env.ctx, model.ListSessionsRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, summaries, 4)
	wantIDs := []model.SessionID{model.DefaultSession, a.ID, b.ID, unnamed.ID}
	for i, s := range summaries {
		assert.Equal(t, wantIDs[i], s.ID, "row %d", i)
		assert.Zero(t, s.SavepointCount, "row %d", i)
		assert.NotNil(t, s.ComposeProjects, "row %d renders [] rather than null", i)
		assert.Empty(t, s.ComposeProjects, "row %d", i)
	}
	assert.Equal(t, model.DefaultSessionName, summaries[0].Name)
	require.DirExists(t, env.sessionDir(a.ID))

	for ref, want := range map[string]model.SessionID{
		"":                            model.DefaultSession,
		model.DefaultSessionName:      model.DefaultSession,
		model.DefaultSession.String(): model.DefaultSession,
		"feat-a":                      a.ID,
		a.ID.String():                 a.ID,
		unnamed.ID.String():           unnamed.ID,
	} {
		got, err := env.sessions.ResolveSession(env.ctx, ref)
		require.NoError(t, err, "resolve %q", ref)
		assert.Equal(t, want, got.ID, "resolve %q", ref)
	}

	deleted, err := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat-a"})
	require.NoError(t, err)
	assert.Equal(t, a.ID, deleted.Session)
	assert.NoDirExists(t, env.sessionDir(a.ID))
	assert.Equal(t, []string{model.DefaultSessionName, "feat-b", ""}, env.names(t))
	_, err = env.sessions.ResolveSession(env.ctx, "feat-a")
	assert.ErrorIs(t, err, model.ErrSessionNotFound)
	_, err = env.sessions.ResolveSession(env.ctx, a.ID.String())
	assert.ErrorIs(t, err, model.ErrSessionNotFound)
}

func TestSession_Create_Refused(t *testing.T) {
	env := newSessionEnv(t)
	taken := env.create(t, "taken")

	tests := []struct {
		name    string
		session string
		wantErr error
	}{
		{name: "blank", session: "   ", wantErr: model.ErrInvalidSessionName},
		{name: "taken", session: "taken", wantErr: model.ErrSessionNameTaken},
		{name: "the default's name", session: model.DefaultSessionName, wantErr: model.ErrSessionNameTaken},
		{name: "another session's id", session: taken.ID.String(), wantErr: model.ErrSessionNameTaken},
		{name: "the default's id", session: model.DefaultSession.String(), wantErr: model.ErrSessionNameTaken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := env.sessions.CreateSession(env.ctx, model.CreateSessionRequest{Name: tt.session})

			// Assert
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, []string{model.DefaultSessionName, "taken"}, env.names(t), "nothing was written")
		})
	}
}

func TestSession_Resolve_NotFound(t *testing.T) {
	env := newSessionEnv(t)
	env.create(t, "feat")

	for _, ref := range []string{"no-such-session", model.NewSessionID().String()} {
		// Act
		_, err := env.sessions.ResolveSession(env.ctx, ref)

		// Assert
		assert.ErrorIs(t, err, model.ErrSessionNotFound, "resolve %q", ref)
	}
}

func TestSession_Delete_Refused(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	feat := env.create(t, "feat")

	// Act
	_, defaultErr := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: model.DefaultSessionName})
	env.marker(t, feat.ID)
	_, markedErr := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})

	// Assert
	assert.ErrorIs(t, defaultErr, model.ErrDefaultSessionDelete)
	var interrupted *model.InterruptedRollbackError
	require.ErrorAs(t, markedErr, &interrupted)
	assert.Equal(t, feat.ID, interrupted.Marker.Session)
	assert.DirExists(t, env.sessionDir(feat.ID), "a refused delete removes nothing")
	assert.Equal(t, []string{model.DefaultSessionName, "feat"}, env.names(t))
}

func TestSession_Delete_PassesAMarkerOfAnotherSession(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	feat := env.create(t, "feat")
	env.marker(t, model.DefaultSession)

	// Act
	deleted, err := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, feat.ID, deleted.Session)
	_, found, err := env.inflight.Get(env.ctx)
	require.NoError(t, err)
	assert.True(t, found, "the other session's marker stays")
}

func TestSession_RefusedWhileLocked(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.create(t, "feat")
	release, err := lock.New(env.projectRoot).Acquire(env.ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	// Act
	_, createErr := env.sessions.CreateSession(env.ctx, model.CreateSessionRequest{Name: "other"})
	_, deleteErr := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})

	// Assert
	assert.ErrorIs(t, createErr, model.ErrProjectLocked)
	assert.ErrorIs(t, deleteErr, model.ErrProjectLocked)
	assert.Equal(t, []string{model.DefaultSessionName, "feat"}, env.names(t))
}

// A session directory with no record is what an interrupted create leaves: it is no session.
func TestSession_IncompleteSessionIsInvisible(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	orphan := model.NewSessionID()
	require.NoError(t, os.MkdirAll(filepath.Join(env.sessionDir(orphan), "compose"), 0o755))

	// Act
	names := env.names(t)
	_, err := env.sessions.ResolveSession(env.ctx, orphan.String())

	// Assert
	assert.Equal(t, []string{model.DefaultSessionName}, names)
	assert.ErrorIs(t, err, model.ErrSessionNotFound)
}

// withComposeProject starts one compose project the session tests share across sessions.
func (e *sessionEnv) withComposeProject(t *testing.T) {
	t.Helper()

	e.daemon = newDaemonClient(t)
	e.project = "arcave-session-it-" + randomSuffix(t)
	t.Cleanup(func() { removeComposeProject(t, e.daemon, e.project) })
	e.in(t, model.DefaultSession).up(t, 1)
}

// in is the compose project as seen from session: a core.Service wired the way
// initAllDependencies wires one, over the same project root, lock and marker.
func (e *sessionEnv) in(t *testing.T, session model.SessionID) *imageRollbackEnv {
	t.Helper()

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	progress.Mode = progress.ModeQuiet

	engine := dockerclient.NewEngine(e.daemon)
	inspector := introspect.NewInspector(e.daemon, model.DockerComposeProjectName(e.project))
	runner := composeclient.NewClient(dockercompose.NewComposeService(cli), composeclient.Options{
		WaitTimeout: 90 * time.Second, StopTimeout: 5 * time.Second,
	})

	root, err := storefactory.Root(e.storage, e.projectRoot)
	require.NoError(t, err)
	arcaveProjectStore, err := storefactory.ArcaveProject(root, sessionArcaveProject)
	require.NoError(t, err)
	store, err := storefactory.New(e.storage, e.projectRoot, sessionArcaveProject, session,
		model.DockerComposeProjectName(e.project))
	require.NoError(t, err)
	blobStore, err := store.SubStorage(dedupcopy.BlobPrefix, true)
	require.NoError(t, err)

	svc := core.New(core.Deps{
		SavepointRepo:      sprepo.New(store),
		ProjectLocker:      lock.New(e.projectRoot),
		ComposeInspector:   inspector,
		ProjectAssembler:   composespec.NewAssembler(),
		SpecCodec:          composespec.NewCodec(),
		ComposeRunner:      runner,
		VolumeRemover:      engine,
		ImageBackend:       imagecopy.NewCopier(store, engine, dedupcopy.New(blobStore)),
		ContainerRebuilder: engine,
		BackendRegistry:    mustRegistry(t, engine),
		Inflight:           e.inflight,
		Store:              store,
		DefaultBackend:     model.SnapshotBackendCopy,
		Session:            session,
		ComposeProject:     model.DockerComposeProjectName(e.project),
		Sessions:           sessionrepo.New(arcaveProjectStore),
	})
	return &imageRollbackEnv{
		ctx:       e.ctx,
		svc:       svc,
		compose:   runner,
		inspector: inspector,
		daemon:    e.daemon,
		store:     store,
		project:   e.project,
	}
}

func TestSession_SavepointsAreIsolated(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	feat := env.create(t, "feat")
	baseline, branch := env.in(t, model.DefaultSession), env.in(t, feat.ID)

	// Act
	fromBaseline, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "v1"})
	require.NoError(t, err)
	fromBranch, err := branch.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "v1"})
	require.NoError(t, err, "a name is unique within its session only")

	// Assert
	baselineList, err := baseline.svc.ListSavepoints(env.ctx, model.ListRequest{})
	require.NoError(t, err)
	branchList, err := branch.svc.ListSavepoints(env.ctx, model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, baselineList, 1)
	require.Len(t, branchList, 1)
	assert.Equal(t, fromBaseline.ID, baselineList[0].ID)
	assert.Equal(t, fromBranch.ID, branchList[0].ID)

	_, err = branch.svc.Rollback(env.ctx, model.RollbackRequest{ID: &fromBaseline.ID})
	assert.Error(t, err, "a session cannot reach another session's savepoint by id")

	summaries, err := env.sessions.ListSessions(env.ctx, model.ListSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	for _, s := range summaries {
		assert.Equal(t, 1, s.SavepointCount, s.Name)
		assert.Equal(t, []model.DockerComposeProjectName{model.DockerComposeProjectName(env.project)},
			s.ComposeProjects, s.Name)
	}
}

func TestSession_RollbackStaysInItsSession(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	feat := env.create(t, "feat")
	baseline, branch := env.in(t, model.DefaultSession), env.in(t, feat.ID)

	baseline.write(t, baseline.replicas(t)[0], "original")
	_, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	baseline.write(t, baseline.replicas(t)[0], "branched")
	_, err = branch.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	baseline.write(t, baseline.replicas(t)[0], "drifted")
	name := "sp"

	// Act
	_, branchErr := branch.svc.Rollback(env.ctx, model.RollbackRequest{Name: &name})
	branchState := baseline.read(t, baseline.replicas(t)[0], volumeMarker) + "/" +
		baseline.read(t, baseline.replicas(t)[0], layerMarker)
	_, baselineErr := baseline.svc.Rollback(env.ctx, model.RollbackRequest{Name: &name})
	baselineState := baseline.read(t, baseline.replicas(t)[0], volumeMarker) + "/" +
		baseline.read(t, baseline.replicas(t)[0], layerMarker)

	// Assert
	require.NoError(t, branchErr)
	require.NoError(t, baselineErr)
	assert.Equal(t, "branched/branched", branchState, "feat's sp")
	assert.Equal(t, "original/original", baselineState, "baseline's sp")
	for _, sp := range []*imageRollbackEnv{baseline, branch} {
		list, err := sp.svc.ListSavepoints(env.ctx, model.ListRequest{IncludeSafetySnapshots: true})
		require.NoError(t, err)
		assert.Len(t, list, 1, "each session holds its own sp and no leftover safety snapshot")
	}
}

func TestSession_DeleteDropsItsDataAndLeavesTheProjectRunning(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	feat := env.create(t, "feat")
	baseline, branch := env.in(t, model.DefaultSession), env.in(t, feat.ID)

	containerID := baseline.replicas(t)[0]
	baseline.write(t, containerID, "original")
	kept, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	baseline.write(t, containerID, "branched")
	_, err = branch.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(env.sessionDir(feat.ID), "compose", env.project, "shared", "oci", "blobs"))

	// Act
	_, err = env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})

	// Assert
	require.NoError(t, err)
	assert.NoDirExists(t, env.sessionDir(feat.ID), "the session's tree, its blobs included")
	assert.Equal(t, []string{containerID}, baseline.replicas(t), "the running container is left alone")
	assert.Equal(t, "branched", baseline.read(t, containerID, volumeMarker), "and so is its volume")

	_, err = branch.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "late"})
	assert.ErrorIs(t, err, model.ErrSessionNotFound, "a service bound to the deleted session writes nothing")
	assert.NoDirExists(t, env.sessionDir(feat.ID))

	_, err = baseline.svc.Rollback(env.ctx, model.RollbackRequest{ID: &kept.ID})
	require.NoError(t, err, "baseline's own blobs survived")
	assert.Equal(t, "original", baseline.read(t, baseline.replicas(t)[0], volumeMarker))
}

// The marker is host-local: an interrupted rollback in one session holds every session.
func TestSession_InterruptedRollbackMarkerSpansSessions(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	feat := env.create(t, "feat")
	baseline := env.in(t, model.DefaultSession)
	env.marker(t, feat.ID)

	// Act
	_, createErr := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	_, deleteErr := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})
	require.NoError(t, env.inflight.Clear(env.ctx))
	_, createAfterErr := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	_, deleteAfterErr := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})

	// Assert
	var interrupted *model.InterruptedRollbackError
	assert.ErrorAs(t, createErr, &interrupted)
	assert.ErrorAs(t, deleteErr, &interrupted)
	assert.NoError(t, createAfterErr)
	assert.NoError(t, deleteAfterErr)
}

// The size a listing reports is the session's tree on disk, everything a capture writes lands
// in that tree, and a delete frees exactly that much. The capture runs on fullcopy only:
// cdc's restic repositories are not covered here.
func TestSession_SizeIsWhatDeleteFrees(t *testing.T) {
	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	baselineBefore := env.summary(t, model.DefaultSession).SizeBytes
	before := fileSizes(t, env.storeDir())
	feat := env.create(t, "feat")
	_, err := env.in(t, feat.ID).svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)

	added := fileSizes(t, env.storeDir())
	var onDisk int64
	for name, size := range added {
		if _, found := before[name]; found {
			continue
		}
		assert.True(t, strings.HasPrefix(name, env.sessionDir(feat.ID)+string(filepath.Separator)),
			"%s lies outside the session's tree", name)
		onDisk += size
	}

	// Act
	listed := env.summary(t, feat.ID)
	deleted, err := env.sessions.DeleteSession(env.ctx, model.DeleteSessionRequest{Session: "feat"})

	// Assert
	require.NoError(t, err)
	assert.Positive(t, listed.SizeBytes)
	assert.Equal(t, onDisk, listed.SizeBytes, "size_bytes is the tree on disk")
	assert.Equal(t, baselineBefore, env.summary(t, model.DefaultSession).SizeBytes, "baseline is unchanged")
	assert.Equal(t, listed.SizeBytes, deleted.FreedBytes, "freed_bytes is what was listed")
	assert.NoDirExists(t, env.sessionDir(feat.ID))
	assert.Equal(t, before, fileSizes(t, env.storeDir()), "the store holds what it held before")
}

// summary is the listing's row for session.
func (e *sessionEnv) summary(t *testing.T, session model.SessionID) model.SessionSummary {
	t.Helper()

	summaries, err := e.sessions.ListSessions(e.ctx, model.ListSessionsRequest{})
	require.NoError(t, err)
	for _, s := range summaries {
		if s.ID == session {
			return s
		}
	}
	require.Failf(t, "session not listed", "%s", session)
	return model.SessionSummary{}
}

// fileSizes maps every regular file under dir to its size.
func fileSizes(t *testing.T, dir string) map[string]int64 {
	t.Helper()

	sizes := map[string]int64{}
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sizes[name] = info.Size()
		return nil
	})
	require.NoError(t, err)
	return sizes
}
