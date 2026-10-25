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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	inflightrepo "github.com/greenmaskio/arcave/internal/repository/inflight"
	sessionrepo "github.com/greenmaskio/arcave/internal/repository/session"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/layertag"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/ociblob"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/ociimage"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/resticrepo"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/savepointdir"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// A dry run names what removed savepoints, rollbacks and dead runs left behind, changes nothing,
// and names nothing live.
func TestGC_DryRun_NamesOnlyTheDead(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	baseline := env.in(t, model.DefaultSession)
	replica := baseline.replicas(t)[0]
	baseline.write(t, replica, "first")
	first, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "first"})
	require.NoError(t, err)
	baseline.write(t, replica, "second")
	second, err := baseline.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "second"})
	require.NoError(t, err)
	// Two rollbacks: the first's tag is left behind unused once the second runs.
	_, err = baseline.svc.Rollback(env.ctx, model.RollbackRequest{ID: &first.ID})
	require.NoError(t, err)
	_, err = baseline.svc.Rollback(env.ctx, model.RollbackRequest{ID: &second.ID})
	require.NoError(t, err)
	_, err = baseline.svc.RemoveSavepoints(env.ctx, model.RemoveRequest{IDs: []model.SavepointID{first.ID}})
	require.NoError(t, err)

	composeDir := filepath.Join(env.sessionDir(model.DefaultSession), "compose", env.project)
	// A fresh tree with no heartbeat and one whose heartbeat reads in progress: both are dead.
	deadTree, staleTree := model.NewSavepointID(), model.NewSavepointID()
	plant(t, filepath.Join(composeDir, deadTree.String(), "volume-named-x", "data.tar"), "partial capture")
	plant(t, filepath.Join(composeDir, staleTree.String(), "volume-named-x", "data.tar"), "capture cut off")
	plantHeartbeat(t, filepath.Join(composeDir, staleTree.String()), heartbeat.StatusInProgress)

	svc := env.newGarbageCollector(t, model.DefaultSession, baseline.engine(), model.GarbageDataKinds()...)
	before := fileSizes(t, env.storeDir())

	// Act
	result, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, before, fileSizes(t, env.storeDir()), "a dry run changes nothing")
	ops := opsByKind(result)
	assert.ElementsMatch(t, []string{deadTree.String(), staleTree.String()}, targetsOf(ops[model.GarbageDataKindSavepointData]),
		"a dead tree goes however young, whatever its heartbeat says")
	assert.NotEmpty(t, ops[model.GarbageDataKindImageLayers], "the removed savepoint and the safety snapshots held blobs of their own")
	var freedBy []model.SavepointID
	for _, dead := range result.Savepoints {
		freedBy = append(freedBy, dead.Savepoint)
	}
	assert.ElementsMatch(t, []model.SavepointID{deadTree, staleTree}, freedBy, "each dead savepoint says what it frees")
	tags := targetsOf(ops[model.GarbageDataKindRestoredLayers])
	assert.Contains(t, strings.Join(tags, ","), ":sp-"+first.ID.String(), "the first rollback's tag is unused")
	assert.NotContains(t, strings.Join(tags, ","), ":sp-"+second.ID.String(), "the running container's tag is in use")

	var reported int64
	for _, kind := range []model.GarbageDataKind{model.GarbageDataKindImageLayers, model.GarbageDataKindUpstreamImages} {
		for _, op := range ops[kind] {
			info, err := os.Stat(filepath.Join(composeDir, filepath.FromSlash(op.Target)))
			require.NoError(t, err, "%s names an object the store holds", op.Target)
			if !info.IsDir() {
				assert.Equal(t, info.Size(), *op.Bytes, op.Target)
			}
			reported += *op.Bytes
		}
	}
	assert.Positive(t, reported)

	// Deleting what was named leaves the surviving savepoint whole.
	for _, kind := range []model.GarbageDataKind{model.GarbageDataKindSavepointData, model.GarbageDataKindUpstreamImages, model.GarbageDataKindImageLayers} {
		for _, op := range ops[kind] {
			require.NoError(t, os.RemoveAll(filepath.Join(composeDir, filepath.FromSlash(op.Target))))
		}
	}
	baseline.write(t, baseline.replicas(t)[0], "drifted")
	_, err = baseline.svc.Rollback(env.ctx, model.RollbackRequest{ID: &second.ID})
	require.NoError(t, err, "nothing a live savepoint needs was named")
	assert.Equal(t, "second", baseline.read(t, baseline.replicas(t)[0], volumeMarker))
	assert.Equal(t, "second", baseline.read(t, baseline.replicas(t)[0], layerMarker))
}

// A gc looks only at its own session's compose projects.
func TestGC_DryRun_StaysInItsSession(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newSessionEnv(t)
	env.withComposeProject(t)
	feat := env.create(t, "feat")
	_, err := env.in(t, feat.ID).svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "sp"})
	require.NoError(t, err)
	deadTree := model.NewSavepointID()
	plant(t, filepath.Join(env.sessionDir(feat.ID), "compose", env.project, deadTree.String(), "x"), "partial")
	baselineSvc := env.newGarbageCollector(t, model.DefaultSession, nil, model.GarbageDataKindSavepointData)
	featSvc := env.newGarbageCollector(t, feat.ID, nil, model.GarbageDataKindSavepointData)

	// Act
	baseline, baselineErr := baselineSvc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})
	inFeat, featErr := featSvc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, baselineErr)
	require.NoError(t, featErr)
	assert.Equal(t, model.DefaultSession, baseline.Session)
	assert.Zero(t, baseline.Total.Found, "feat's dead tree is not baseline's")
	assert.Equal(t, []string{deadTree.String()}, targetsOf(opsByKind(inFeat)[model.GarbageDataKindSavepointData]))
}

// newGarbageCollector wires a gc over session the way newGCDependencies does, running kinds.
func (e *sessionEnv) newGarbageCollector(
	t *testing.T, session model.SessionID, engine *dockerclient.Engine, kinds ...model.GarbageDataKind,
) *gc.GarbageCollector {
	t.Helper()

	root, err := storefactory.Root(t.Context(), e.storage, e.projectRoot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	arcaveProjectStore, err := storefactory.ArcaveProject(root, sessionArcaveProject)
	require.NoError(t, err)

	var daemonCollectors []gc.DaemonCollector
	var composeProjectCollectors []gc.ComposeProjectCollector
	for _, kind := range kinds {
		switch kind {
		case model.GarbageDataKindSavepointData:
			composeProjectCollectors = append(composeProjectCollectors, savepointdir.New())
		case model.GarbageDataKindUpstreamImages:
			composeProjectCollectors = append(composeProjectCollectors, ociimage.New())
		case model.GarbageDataKindImageLayers:
			composeProjectCollectors = append(composeProjectCollectors, ociblob.New(2))
		case model.GarbageDataKindDeduplicatedVolumes:
			require.NotNil(t, engine, "the restic collector runs restic in the daemon")
			composeProjectCollectors = append(composeProjectCollectors, newResticCollector(t, engine))
		case model.GarbageDataKindRestoredLayers:
			require.NotNil(t, engine, "the restored-layer collector reads the daemon")
			daemonCollectors = append(daemonCollectors, layertag.New(engine, sessionArcaveProject))
		}
	}
	return gc.New(gc.Deps{
		Store:                    arcaveProjectStore,
		Session:                  session,
		Sessions:                 sessionrepo.New(arcaveProjectStore),
		NewSavepoints:            newGCSavepointReader,
		Locker:                   lock.New(e.projectRoot),
		SessionLocker:            newSessionLocker(t, arcaveProjectStore, session),
		Inflight:                 inflightrepo.New(e.projectRoot),
		DaemonCollectors:         daemonCollectors,
		ComposeProjectCollectors: composeProjectCollectors,
		UnlistedNamespaces:       []string{cdc.NamespaceRepos},
	})
}

// A cdc savepoint whose record is gone — a remove interrupted past its record — leaves its
// snapshots for a forget, found by their tag, with restic's estimate of what a prune frees.
func TestGC_DryRun_ForgetsTheSnapshotsOfADeadSavepoint(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newCDCEnv(t, config.CDCBackend{
		RestoreMode: config.RestoreModeWipe,
		Overwrite:   config.OverwriteIfChanged,
		Image:       config.DefaultResticImage,
	})
	first, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "first"})
	require.NoError(t, err)
	env.mutate(t, env.volumes[0])
	_, err = env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "second"})
	require.NoError(t, err)
	require.NoError(t, env.composeProjectStore.Delete(env.ctx, first.ID.String()+"/savepoint.json"))
	before, err := env.composeProjectStore.List(env.ctx, cdc.NamespaceRepos)
	require.NoError(t, err)

	root, err := storefactory.Root(t.Context(), env.storageCfg, env.projectRoot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	arcaveProjectStore, err := storefactory.ArcaveProject(root, cdcArcaveProject)
	require.NoError(t, err)
	svc := gc.New(gc.Deps{
		Store:                    arcaveProjectStore,
		Sessions:                 sessionrepo.New(arcaveProjectStore),
		NewSavepoints:            newGCSavepointReader,
		Locker:                   lock.New(env.projectRoot),
		SessionLocker:            newSessionLocker(t, arcaveProjectStore, model.DefaultSession),
		Inflight:                 inflightrepo.New(env.projectRoot),
		ComposeProjectCollectors: []gc.ComposeProjectCollector{newResticCollector(t, env.engine)},
		UnlistedNamespaces:       []string{cdc.NamespaceRepos},
	})

	// Act
	result, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})

	// Assert
	require.NoError(t, err)
	require.Len(t, result.Reports, 1)
	stat, ok := result.Reports[0].Detail.(model.ResticRepositoryStat)
	require.True(t, ok)
	assert.Len(t, stat.Ops, len(env.volumes), "one snapshot of the dead savepoint per volume")
	for _, op := range stat.Ops {
		assert.Equal(t, model.GarbageActionForget, op.Action)
		assert.Equal(t, first.ID, op.Savepoint)
	}
	assert.Equal(t, len(env.volumes), stat.LiveSnapshots, "the second savepoint's snapshots are live")
	assert.Equal(t, len(env.volumes), stat.LiveRepos)
	assert.Len(t, stat.Estimates, len(env.volumes))
	require.NotNil(t, result.Reports[0].Summary.Bytes, "restic estimates every repository")
	assert.Positive(t, *result.Reports[0].Summary.Bytes, "the mutated volume's old chunks are the first savepoint's alone")
	require.Len(t, result.Savepoints, 1)
	assert.Equal(t, first.ID, result.Savepoints[0].Savepoint)
	after, err := env.composeProjectStore.List(env.ctx, cdc.NamespaceRepos)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a dry run changes no repository")
}

// newResticCollector is the restic collector over engine, its repositories served the way the
// DI root serves them.
func newResticCollector(t *testing.T, engine *dockerclient.Engine) *resticrepo.Collector {
	t.Helper()
	hostNetwork, containerHost := cdcTransport(t, engine)
	return resticrepo.New(engine, resticOpener{
		engine:        engine,
		hostNetwork:   hostNetwork,
		containerHost: containerHost,
	})
}

// resticOpener starts a restic server over a compose project's repositories, per scope.
type resticOpener struct {
	engine        *dockerclient.Engine
	hostNetwork   bool
	containerHost string
}

func (o resticOpener) Open(ctx context.Context, store storages.Navigator) (resticrepo.Inspector, error) {
	resticRoot, err := store.SubStorage(cdc.NamespaceRepos, true)
	if err != nil {
		return nil, err
	}
	server := rest.NewEcho(ctx, repo.NewResolver(resticRoot))
	port, err := rest.Serve(server, "")
	if err != nil {
		return nil, err
	}
	return resticInspector{
		Inspector: cdc.NewInspector(o.engine, rest.RepositoryURLBase("", o.containerHost, port), cdc.Options{
			Image:       config.DefaultResticImage,
			HostNetwork: o.hostNetwork,
		}),
		server: server,
	}, nil
}

// resticInspector is a cdc.Inspector with the server it reads through.
type resticInspector struct {
	*cdc.Inspector
	server *echo.Echo
}

func (i resticInspector) Close(context.Context) { _ = i.server.Close() }

// plantHeartbeat writes a capture's heartbeat at the root of the tree at dir.
func plantHeartbeat(t *testing.T, dir string, status heartbeat.Status) {
	t.Helper()
	body, err := json.Marshal(heartbeat.Heartbeat{
		Operation: model.HeartbeatOperationSavepoint, Status: status,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	plant(t, filepath.Join(dir, heartbeat.FileName), string(body))
}

// engine is the daemon as the restored-layer collector reads it.
func (e *imageRollbackEnv) engine() *dockerclient.Engine {
	return dockerclient.NewEngine(e.daemon)
}

func plant(t *testing.T, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
	require.NoError(t, os.WriteFile(name, []byte(body), 0o644))
}

func opsByKind(result model.GarbageCollectResult) map[model.GarbageDataKind][]model.GarbageOp {
	ops := map[model.GarbageDataKind][]model.GarbageOp{}
	for _, report := range result.Reports {
		if report.Detail == nil {
			continue
		}
		switch stat := report.Detail.(type) {
		case model.SavepointDirectoryStat:
			ops[report.DataKind] = append(ops[report.DataKind], stat.Ops...)
		case model.OCIImageStat:
			ops[report.DataKind] = append(ops[report.DataKind], stat.Ops...)
		case model.OCIBlobStat:
			ops[report.DataKind] = append(ops[report.DataKind], stat.Ops...)
		case model.ResticRepositoryStat:
			ops[report.DataKind] = append(ops[report.DataKind], stat.Ops...)
		case model.DockerLayerTagStat:
			ops[report.DataKind] = append(ops[report.DataKind], stat.Ops...)
		}
	}
	return ops
}

func targetsOf(ops []model.GarbageOp) []string {
	targets := make([]string, 0, len(ops))
	for _, op := range ops {
		targets = append(targets, op.Target)
	}
	return targets
}
