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
	"slices"
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
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/cdc"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/repo"
	"github.com/greenmaskio/arcave/internal/service/gc"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/layertag"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/ociblob"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/ociimage"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/resticrepo"
	"github.com/greenmaskio/arcave/internal/service/gc/collector/savepointdir"
	"github.com/greenmaskio/arcave/internal/service/gc/eraser"
	"github.com/greenmaskio/arcave/internal/service/heartbeat"
	"github.com/greenmaskio/arcave/internal/service/lock"
	"github.com/greenmaskio/arcave/internal/storefactory"
)

// A gc deletes what removed savepoints, rollbacks and dead runs left in the store and the daemon,
// and nothing a live savepoint needs; the blobs of an image it removes go on the run after.
func TestGC_DeletesOnlyTheDead(t *testing.T) {
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
	_, err = baseline.svc.RemoveSavepoints(env.ctx, model.RemoveRequest{Refs: []string{first.ID.String()}})
	require.NoError(t, err)

	composeDir := filepath.Join(env.sessionDir(model.DefaultSession), "compose", env.project)
	// A fresh tree with no heartbeat and one whose heartbeat reads in progress: both are dead.
	deadTree, staleTree := model.NewSavepointID(), model.NewSavepointID()
	plant(t, filepath.Join(composeDir, deadTree.String(), "volume-named-x", "data.tar"), "partial capture")
	plant(t, filepath.Join(composeDir, staleTree.String(), "volume-named-x", "data.tar"), "capture cut off")
	plantHeartbeat(t, filepath.Join(composeDir, staleTree.String()), heartbeat.StatusInProgress)
	// An upstream image no record names, whose manifest alone lists a blob.
	orphanImage, orphanBlob := plantUnreferencedImage(t, composeDir)

	svc := env.newGarbageCollector(t, model.DefaultSession, baseline.engine(), model.GarbageDataKinds()...)
	// Each image and blob op names a file the store holds, at its size.
	preview, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})
	require.NoError(t, err)
	for _, kind := range []model.GarbageDataKind{model.GarbageDataKindImageLayers, model.GarbageDataKindUpstreamImages} {
		for _, op := range opsByKind(preview)[kind] {
			info, err := os.Stat(filepath.Join(composeDir, filepath.FromSlash(op.Target)))
			require.NoError(t, err, "%s names an object the store holds", op.Target)
			if !info.IsDir() {
				assert.Equal(t, info.Size(), *op.Bytes, op.Target)
			}
		}
	}
	before := fileSizes(t, env.storeDir())

	// Act
	result, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{})

	// Assert
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	ops := opsByKind(result)
	assert.ElementsMatch(t, []string{deadTree.String(), staleTree.String()}, targetsOf(ops[model.GarbageDataKindSavepointData]),
		"a dead tree goes however young, whatever its heartbeat says")
	assert.Contains(t, targetsOf(ops[model.GarbageDataKindUpstreamImages]), orphanImage)
	assert.NotEmpty(t, ops[model.GarbageDataKindImageLayers], "the removed savepoint and the safety snapshots held blobs of their own")
	assert.NotContains(t, targetsOf(ops[model.GarbageDataKindImageLayers]), orphanBlob, "an unreferenced image's blob waits for its manifest to go")
	tags := targetsOf(ops[model.GarbageDataKindRestoredLayers])
	assert.Contains(t, strings.Join(tags, ","), ":sp-"+first.ID.String(), "the first rollback's tag is unused")
	assert.NotContains(t, strings.Join(tags, ","), ":sp-"+second.ID.String(), "the running container's tag is in use")

	var (
		deleted []string
		freed   int64
	)
	for _, report := range result.Reports {
		switch report.DataKind {
		case model.GarbageDataKindRestoredLayers:
			require.NotNil(t, report.Deletion)
			assert.Empty(t, report.Deletion.Failed)
			assert.Len(t, report.Deletion.Deleted, report.Summary.Found)
			for _, tag := range tags {
				exists, err := baseline.engine().ImageRefExists(env.ctx, tag)
				require.NoError(t, err)
				assert.False(t, exists, "%s is untagged", tag)
			}
		case model.GarbageDataKindDeduplicatedVolumes:
			require.Empty(t, report.Detail.Findings().Ops, "no savepoint here is cdc's")
			assert.Equal(t, &model.GarbageDeletion{}, report.Deletion, "a report that found nothing has nothing left to delete")
		default:
			require.NotNil(t, report.Deletion, report.DataKind)
			assert.Empty(t, report.Deletion.Failed, report.DataKind)
			assert.Zero(t, report.Deletion.NotRun, report.DataKind)
			assert.Len(t, report.Deletion.Deleted, report.Summary.Found, report.DataKind)
			for _, op := range report.Detail.Findings().Ops {
				deleted = append(deleted, op.Target)
			}
			require.NotNil(t, report.Deletion.FreedBytes(), report.DataKind)
			freed += *report.Deletion.FreedBytes()
		}
	}
	assert.Empty(t, result.Warnings)
	after := fileSizes(t, env.storeDir())
	assert.Equal(t, newWithoutTargets(before, composeDir, deleted), after, "exactly what was named is gone")
	assert.Equal(t, sumSizes(before)-sumSizes(after), freed)
	assert.Nil(t, result.Total.FreedBytes, "what an untag frees in the daemon is not known")
	assert.Equal(t, len(deleted)+len(tags), result.Total.Deleted)

	// The run after frees the blob only the removed image listed.
	secondRun, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{})
	require.NoError(t, err)
	secondOps := opsByKind(secondRun)
	assert.Equal(t, []string{orphanBlob}, targetsOf(secondOps[model.GarbageDataKindImageLayers]))
	assert.Empty(t, secondOps[model.GarbageDataKindSavepointData])
	assert.Empty(t, secondOps[model.GarbageDataKindUpstreamImages])
	assert.NoFileExists(t, filepath.Join(composeDir, filepath.FromSlash(orphanBlob)))

	// And the one after that finds nothing left in the store, and changes nothing.
	beforeThird := fileSizes(t, env.storeDir())
	thirdRun, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, beforeThird, fileSizes(t, env.storeDir()), "a dry run changes nothing")
	thirdOps := opsByKind(thirdRun)
	for _, kind := range []model.GarbageDataKind{
		model.GarbageDataKindSavepointData, model.GarbageDataKindUpstreamImages, model.GarbageDataKindImageLayers,
	} {
		assert.Empty(t, thirdOps[kind], kind)
	}

	// The surviving savepoint is whole.
	baseline.write(t, baseline.replicas(t)[0], "drifted")
	_, err = baseline.svc.Rollback(env.ctx, model.RollbackRequest{ID: &second.ID})
	require.NoError(t, err, "nothing a live savepoint needs was deleted")
	assert.Equal(t, "second", baseline.read(t, baseline.replicas(t)[0], volumeMarker))
	assert.Equal(t, "second", baseline.read(t, baseline.replicas(t)[0], layerMarker))
}

// plantUnreferencedImage stores, under composeDir, an upstream image no record names whose intact
// manifest lists one blob of its own; it returns both keys.
func plantUnreferencedImage(t *testing.T, composeDir string) (image, blob string) {
	t.Helper()
	imageHex, blobHex := strings.Repeat("e", 64), strings.Repeat("f", 64)
	image, blob = "shared/oci/images/sha256/"+imageHex, "shared/oci/blobs/sha256/"+blobHex
	manifest, err := json.Marshal(imagecopy.Manifest{
		ImageID: model.ImageDigestID("sha256:" + imageHex),
		Format:  imagecopy.FormatBlobs,
		Members: []imagecopy.Member{{Name: "blobs/sha256/" + blobHex, Kind: imagecopy.KindBlob, Digest: blobHex}},
	})
	require.NoError(t, err)
	plant(t, filepath.Join(composeDir, filepath.FromSlash(image), "image.json"), string(manifest))
	plant(t, filepath.Join(composeDir, filepath.FromSlash(blob)), "orphan layer")
	return image, blob
}

// newWithoutTargets is sizes without the files under dir at each of targets, or under them.
func newWithoutTargets(sizes map[string]int64, dir string, targets []string) map[string]int64 {
	kept := map[string]int64{}
	for name, size := range sizes {
		gone := slices.ContainsFunc(targets, func(target string) bool {
			key := filepath.Join(dir, filepath.FromSlash(target))
			return name == key || strings.HasPrefix(name, key+string(filepath.Separator))
		})
		if !gone {
			kept[name] = size
		}
	}
	return kept
}

func sumSizes(sizes map[string]int64) int64 {
	var total int64
	for _, size := range sizes {
		total += size
	}
	return total
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

	var (
		daemonCollectors         []gc.DaemonCollector
		composeProjectCollectors []gc.ComposeProjectCollector
		daemonErasers            []gc.DaemonEraser
		composeProjectErasers    = []gc.ComposeProjectEraser{
			eraser.NewTreeEraser(), eraser.NewImageEraser(), eraser.NewObjectEraser(),
		}
	)
	if engine != nil {
		daemonErasers = append(daemonErasers, eraser.NewTagEraser(engine))
		composeProjectErasers = append(composeProjectErasers, newForgetEraser(t, engine))
	}
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
		DaemonErasers:            daemonErasers,
		ComposeProjectErasers:    composeProjectErasers,
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

// A removed cdc savepoint leaves its snapshots in their repositories; a gc forgets them and prunes
// what they alone held, and the savepoint that is left still rolls back.
func TestGC_ForgetsTheSnapshotsOfARemovedSavepoint(t *testing.T) {
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
	second, err := env.svc.CreateSavepoint(env.ctx, model.CreateSavepointRequest{Name: "second"})
	require.NoError(t, err)
	kept := env.hashAll(t)
	_, err = env.svc.RemoveSavepoints(env.ctx, model.RemoveRequest{Refs: []string{first.ID.String()}})
	require.NoError(t, err)
	for _, stat := range first.Stats {
		exists, err := env.composeProjectStore.Exists(env.ctx,
			cdc.NewSnapshotKey(stat.Volume.ObjectID, resticPayload(t, stat).SnapshotObjectID))
		require.NoError(t, err)
		require.True(t, exists, "a remove leaves the snapshot to a gc")
	}
	before := repoBytes(t, env)
	svc := newCDCGarbageCollector(t, env)

	// Act
	result, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{})

	// Assert
	require.NoError(t, err)
	require.Len(t, result.Reports, 1)
	report := result.Reports[0]
	require.NotNil(t, report.Deletion)
	assert.Len(t, report.Deletion.Deleted, len(env.volumes), "one snapshot of the removed savepoint per volume")
	assert.Empty(t, report.Deletion.Failed)
	require.Len(t, report.Deletion.Reclaimed, len(env.volumes), "one prune per repository")
	freed := report.Deletion.FreedBytes()
	require.NotNil(t, freed, "restic reports what every prune freed")
	assert.Positive(t, *freed, "the mutated volume's old chunks were the first savepoint's alone")
	assert.Less(t, repoBytes(t, env), before, "the prune shrank the repositories")
	for _, stat := range first.Stats {
		exists, err := env.composeProjectStore.Exists(env.ctx,
			cdc.NewSnapshotKey(stat.Volume.ObjectID, resticPayload(t, stat).SnapshotObjectID))
		require.NoError(t, err)
		assert.False(t, exists, "the snapshot is forgotten")
	}

	// The run after finds nothing left to forget.
	again, err := svc.GarbageCollect(env.ctx, model.GarbageCollectRequest{DryRun: true})
	require.NoError(t, err)
	stat, ok := again.Reports[0].Detail.(model.ResticRepositoryStat)
	require.True(t, ok)
	assert.Empty(t, stat.Ops)
	assert.Equal(t, len(env.volumes), stat.LiveSnapshots)

	// The savepoint left is whole.
	env.mutate(t, env.volumes[0])
	_, err = env.svc.Rollback(env.ctx, model.RollbackRequest{ID: &second.ID})
	require.NoError(t, err)
	assert.Equal(t, kept, env.hashAll(t), "nothing the live savepoint needs was pruned")
}

// newCDCGarbageCollector is a gc over env's default session, judging and deleting its restic
// snapshots only.
func newCDCGarbageCollector(t *testing.T, env *cdcEnv) *gc.GarbageCollector {
	t.Helper()
	arcaveProjectStore := env.arcaveProjectStore(t)
	return gc.New(gc.Deps{
		Store:                    arcaveProjectStore,
		Sessions:                 sessionrepo.New(arcaveProjectStore),
		NewSavepoints:            newGCSavepointReader,
		Locker:                   lock.New(env.projectRoot),
		SessionLocker:            newSessionLocker(t, arcaveProjectStore, model.DefaultSession),
		Inflight:                 inflightrepo.New(env.projectRoot),
		ComposeProjectCollectors: []gc.ComposeProjectCollector{newResticCollector(t, env.engine)},
		ComposeProjectErasers:    []gc.ComposeProjectEraser{newForgetEraser(t, env.engine)},
		UnlistedNamespaces:       []string{cdc.NamespaceRepos},
	})
}

// repoBytes sums the sizes of every object of env's restic repositories.
func repoBytes(t *testing.T, env *cdcEnv) int64 {
	t.Helper()
	objects, err := env.composeProjectStore.List(env.ctx, cdc.NamespaceRepos)
	require.NoError(t, err)
	var total int64
	for _, obj := range objects {
		total += obj.Size
	}
	return total
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

// newForgetEraser is the forget eraser over engine, its repositories served the way the DI root
// serves them.
func newForgetEraser(t *testing.T, engine *dockerclient.Engine) eraser.ForgetEraser {
	t.Helper()
	hostNetwork, containerHost := cdcTransport(t, engine)
	return eraser.NewForgetEraser(resticPrunerOpener{resticOpener{
		engine:        engine,
		hostNetwork:   hostNetwork,
		containerHost: containerHost,
	}}, 1)
}

// resticOpener starts a restic server over a compose project's repositories, per scope.
type resticOpener struct {
	engine        *dockerclient.Engine
	hostNetwork   bool
	containerHost string
}

func (o resticOpener) Open(ctx context.Context, store storages.Navigator) (resticrepo.Inspector, error) {
	_, repoURLBase, server, err := o.serve(ctx, store)
	if err != nil {
		return nil, err
	}
	return resticInspector{
		Inspector: cdc.NewInspector(o.engine, repoURLBase, o.options()),
		server:    server,
	}, nil
}

// serve starts the restic server over store's repositories.
func (o resticOpener) serve(
	ctx context.Context, store storages.Navigator,
) (root storages.Storager, repoURLBase string, server *echo.Echo, err error) {
	if root, err = store.SubStorage(cdc.NamespaceRepos, true); err != nil {
		return nil, "", nil, err
	}
	server = rest.NewEcho(ctx, repo.NewResolver(root))
	port, err := rest.Serve(server, "")
	if err != nil {
		return nil, "", nil, err
	}
	return root, rest.RepositoryURLBase("", o.containerHost, port), server, nil
}

func (o resticOpener) options() cdc.Options {
	return cdc.Options{
		Image:       config.DefaultResticImage,
		HostNetwork: o.hostNetwork,
	}
}

// resticInspector is a cdc.Inspector with the server it reads through.
type resticInspector struct {
	*cdc.Inspector
	server *echo.Echo
}

func (i resticInspector) Close(context.Context) { _ = i.server.Close() }

// resticPrunerOpener starts a restic server over a compose project's repositories, per erase.
type resticPrunerOpener struct{ resticOpener }

func (o resticPrunerOpener) Open(ctx context.Context, store storages.Navigator) (eraser.Pruner, error) {
	root, repoURLBase, server, err := o.serve(ctx, store)
	if err != nil {
		return nil, err
	}
	return resticPruner{
		Pruner: cdc.NewPruner(o.engine, root, repoURLBase, o.options()),
		server: server,
	}, nil
}

// resticPruner is a cdc.Pruner with the server it prunes through.
type resticPruner struct {
	*cdc.Pruner
	server *echo.Echo
}

func (p resticPruner) Close(context.Context) { _ = p.server.Close() }

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
