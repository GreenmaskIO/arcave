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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/service/core"
	"github.com/greenmaskio/arcave/internal/utils"
)

// newUnreachableDaemonConfig is a project over a directory store and a daemon that does not answer.
func newUnreachableDaemonConfig(t *testing.T, projectPath string) config.Config {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	cfg := config.Config{
		Project: config.Project{Name: "app"},
		Storage: config.Storage{
			Type:      config.StorageTypeDirectory,
			Directory: &config.DirectoryStorage{Path: t.TempDir()},
		},
		ComposeProvider: composeconfig.Config{
			Docker: composeconfig.Docker{Host: "tcp://127.0.0.1:1"},
		},
	}
	cfg.ApplyDefaults(projectPath)
	return cfg
}

// savepoint list and remove reach no daemon and build no backend: against a daemon that does not
// answer and a compression no factory takes, a savepoint of any backend is listed and removed whole,
// in a session of its own.
func TestOpenSavepointStoreService(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	projectPath := t.TempDir()
	cfg := newUnreachableDaemonConfig(t, projectPath)
	cfg.ComposeProvider.Backends.FullCopy.Compression = "lz4"

	var opened utils.Closers
	t.Cleanup(func() { opened.Release(ctx) })
	sessions, err := openSessionService(ctx, projectPath, cfg, &opened)
	require.NoError(t, err)
	_, err = sessions.CreateSession(ctx, model.CreateSessionRequest{Name: "exp"})
	require.NoError(t, err)

	scope, err := openComposeProjectScope(ctx, cfg, projectPath, "exp", "demo", &opened)
	require.NoError(t, err)
	savepoint := model.Savepoint{
		ID: "019e843e-1650-7000-8000-000000000000",
		Stats: []model.SnapshotStat{
			{Volume: model.Volume{ObjectID: "volume-named-a", Backend: model.SnapshotBackendFullCopy}},
			{Volume: model.Volume{ObjectID: "volume-named-b", Backend: model.SnapshotBackendCDC}},
			{Volume: model.Volume{ObjectID: "volume-named-c", Backend: "retired"}},
		},
	}
	putSavepointRecord(ctx, t, scope.store, savepoint)
	archive := savepoint.ID.String() + "/volume-named-a/data.tar"
	require.NoError(t, scope.store.PutObject(ctx, archive, strings.NewReader("archive")))

	svc, err := openSavepointStoreService(ctx, projectPath, cfg, "exp", "demo", &opened)
	require.NoError(t, err)

	// Act
	listed, listErr := svc.ListSavepoints(ctx, model.ListRequest{Sort: model.SortAsc})
	removed, removeErr := svc.RemoveSavepoints(ctx, model.RemoveRequest{Refs: []string{savepoint.ID.String()}})
	left, leftErr := svc.ListSavepoints(ctx, model.ListRequest{Sort: model.SortAsc})

	// Assert
	require.NoError(t, listErr)
	require.NoError(t, removeErr)
	require.NoError(t, leftErr)
	require.Len(t, listed, 1)
	assert.Equal(t, savepoint.ID, listed[0].ID)
	require.Len(t, removed.Removed, 1)
	assert.Equal(t, savepoint.ID, removed.Removed[0].ID)
	assert.Empty(t, left)
	exists, err := scope.store.Exists(ctx, archive)
	require.NoError(t, err)
	assert.False(t, exists, "the tree goes with the record")
}

// A workflow that needs the daemon fails on the wiring's first request.
func TestOpenSavepointService_DaemonUnreachable(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	projectPath := t.TempDir()
	cfg := newUnreachableDaemonConfig(t, projectPath)
	var opened utils.Closers
	t.Cleanup(func() { opened.Release(ctx) })

	// Act
	svc, err := openSavepointService(ctx, projectPath, cfg, "", "demo", &opened)

	// Assert
	require.ErrorContains(t, err, "read the daemon version")
	assert.Nil(t, svc)
}

// A gc reads no profile: a daemon that does not answer is its collectors' probe, not the wiring's failure.
func TestOpenGCService_DaemonUnreachable(t *testing.T) {
	// Arrange
	ctx := zerolog.Nop().WithContext(context.Background())
	projectPath := t.TempDir()
	cfg := newUnreachableDaemonConfig(t, projectPath)
	var opened utils.Closers
	t.Cleanup(func() { opened.Release(ctx) })

	// Act
	svc, err := openGCService(ctx, projectPath, cfg, "", &opened)

	// Assert
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

// gc.safety_snapshots.retain_recent reaches the retention, which removes the safety snapshots past
// it, record and tree, with no daemon.
func TestOpenGCService_RetainsTheSafetySnapshots(t *testing.T) {
	var (
		labels = map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue}
		oldest = model.SavepointID("019e843e-1650-7000-8000-00000000000a")
		newest = model.SavepointID("019e843e-1650-7000-8000-00000000000c")
		plain  = model.SavepointID("019e843e-1650-7000-8000-00000000000d")
	)
	tests := []struct {
		name         string
		retainRecent int
		wantLeft     []model.SavepointID
	}{
		{name: "0 deletes every one", retainRecent: 0, wantLeft: []model.SavepointID{plain}},
		{name: "1 keeps the newest", retainRecent: 1, wantLeft: []model.SavepointID{newest, plain}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			projectPath := t.TempDir()
			cfg := newUnreachableDaemonConfig(t, projectPath)
			cfg.GC.SafetySnapshots.RetainRecent = &tt.retainRecent
			var opened utils.Closers
			t.Cleanup(func() { opened.Release(ctx) })
			scope, err := openComposeProjectScope(ctx, cfg, projectPath, "", "demo", &opened)
			require.NoError(t, err)
			for _, savepoint := range []model.Savepoint{{ID: oldest, Labels: labels}, {ID: newest, Labels: labels}, {ID: plain}} {
				putSavepointRecord(ctx, t, scope.store, savepoint)
			}
			data := oldest.String() + "/volume-named-a/data.tar"
			require.NoError(t, scope.store.PutObject(ctx, data, strings.NewReader("archive")))
			svc, err := openGCService(ctx, projectPath, cfg, "", &opened)
			require.NoError(t, err)

			// Act
			_, err = svc.GarbageCollect(ctx, model.GarbageCollectRequest{
				DataKinds: []model.GarbageDataKind{model.GarbageDataKindSafetySnapshots},
			})

			// Assert
			require.NoError(t, err)
			left, err := compose.NewStoreSavepoints(scope.store).List(ctx)
			require.NoError(t, err)
			var ids []model.SavepointID
			for _, savepoint := range left {
				ids = append(ids, savepoint.ID)
			}
			assert.Equal(t, tt.wantLeft, ids)
			exists, err := scope.store.Exists(ctx, data)
			require.NoError(t, err)
			assert.False(t, exists, "the tree goes with the record")
		})
	}
}

func TestNewMCPFactories(t *testing.T) {
	// Act
	factories := reflect.ValueOf(newMCPFactories())

	// Assert
	for i := range factories.NumField() {
		assert.False(t, factories.Field(i).IsNil(), factories.Type().Field(i).Name)
	}
}

func TestOpenArcaveProjectStore(t *testing.T) {
	key := newTestStoreKey(t)
	encrypted := func(t *testing.T, projectPath string) {
		t.Helper()
		var opened utils.Closers
		defer opened.Release(t.Context())
		created, err := openArcaveProjectStore(t.Context(),
			newEncryptionConfig(t, projectPath, config.Encryption{Enabled: true, Key: key}), projectPath, &opened)
		require.NoError(t, err)
		require.NoError(t, created.writeAdmission.AdmitWrite(t.Context()))
		require.NoError(t, created.store.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("a secret")))
	}
	plain := func(t *testing.T, projectPath string) {
		t.Helper()
		var opened utils.Closers
		defer opened.Release(t.Context())
		created, err := openArcaveProjectStore(t.Context(), newEncryptionConfig(t, projectPath, config.Encryption{}), projectPath, &opened)
		require.NoError(t, err)
		require.NoError(t, created.writeAdmission.AdmitWrite(t.Context()))
		require.NoError(t, created.store.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("{}")))
	}
	tests := []struct {
		name       string
		stored     func(t *testing.T, projectPath string)
		encryption config.Encryption
		wantErr    error
		wantMsg    string
		verify     func(t *testing.T, projectPath string, store storages.Storager)
	}{
		{
			name:       "an encrypted store with the key it was created with",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true, Key: key},
			verify: func(t *testing.T, projectPath string, store storages.Storager) {
				assert.Equal(t, "a secret", readStoreObject(t, store, "sessions/x/session.json"))
				raw, err := os.ReadFile(filepath.Join(arcaveProjectPath(projectPath), "sessions", "x", "session.json"))
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "a secret")
			},
		},
		{
			name:       "an encrypted store with another key",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true, Key: newTestStoreKey(t)},
			wantErr:    model.ErrWrongStoreKey,
			wantMsg:    "check encryption.key",
		},
		{
			name:       "an encrypted store with no key",
			stored:     encrypted,
			encryption: config.Encryption{Enabled: true},
			wantErr:    model.ErrInvalidStoreKey,
			wantMsg:    "none set (check encryption.key",
		},
		{
			name:    "an encrypted store with encryption off",
			stored:  encrypted,
			wantErr: model.ErrStoreEncrypted,
			wantMsg: "set encryption.enabled: true",
		},
		{
			name:   "a plain store with encryption off",
			stored: plain,
			verify: func(t *testing.T, _ string, store storages.Storager) {
				assert.Equal(t, "{}", readStoreObject(t, store, "sessions/x/session.json"))
			},
		},
		{
			name:       "a plain store with encryption on",
			stored:     plain,
			encryption: config.Encryption{Enabled: true, Key: key},
			wantErr:    model.ErrStoreHoldsPlainData,
			wantMsg:    "turn encryption.enabled off to use it",
		},
		{
			name:       "a plain store with the key exported and encryption off",
			stored:     plain,
			encryption: config.Encryption{Key: key},
			verify: func(t *testing.T, _ string, store storages.Storager) {
				assert.Equal(t, "{}", readStoreObject(t, store, "sessions/x/session.json"), "the key is ignored")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			projectPath := t.TempDir()
			tt.stored(t, projectPath)
			before := listTree(t, arcaveProjectPath(projectPath))

			var opened utils.Closers
			t.Cleanup(func() { opened.Release(context.Background()) })

			// Act
			project, err := openArcaveProjectStore(t.Context(), newEncryptionConfig(t, projectPath, tt.encryption), projectPath, &opened)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantMsg)
				if tt.encryption.Key != "" {
					assert.NotContains(t, err.Error(), tt.encryption.Key)
				}
				assert.Equal(t, before, listTree(t, arcaveProjectPath(projectPath)), "a refusal writes nothing")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, before, listTree(t, arcaveProjectPath(projectPath)), "an open writes nothing")
			tt.verify(t, projectPath, project.store)
		})
	}
}

// An open writes nothing under encryption; only the admission a writing workflow asks for marks the
// project, and only an empty one.
func TestOpenedArcaveProject_writeAdmission(t *testing.T) {
	key := newTestStoreKey(t)
	tests := []struct {
		name       string
		marked     string // the key the project is marked for; empty for none
		plainData  bool   // the project holds a session written without encryption
		wantErr    error
		wantMarked bool // AdmitWrite writes the marker
	}{
		{name: "an empty project is marked by a write's admission alone", wantMarked: true},
		{name: "a project marked for the key", marked: key},
		{name: "a project marked for another key", marked: newTestStoreKey(t), wantErr: model.ErrWrongStoreKey},
		{name: "a project holding plain data", plainData: true, wantErr: model.ErrStoreHoldsPlainData},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			projectPath := t.TempDir()
			var setup utils.Closers
			if tt.marked != "" {
				project, err := openArcaveProjectStore(t.Context(),
					newEncryptionConfig(t, projectPath, config.Encryption{Enabled: true, Key: tt.marked}), projectPath, &setup)
				require.NoError(t, err)
				require.NoError(t, project.writeAdmission.AdmitWrite(t.Context()))
			}
			if tt.plainData {
				project, err := openArcaveProjectStore(t.Context(),
					newEncryptionConfig(t, projectPath, config.Encryption{}), projectPath, &setup)
				require.NoError(t, err)
				require.NoError(t, project.store.PutObject(t.Context(), "sessions/x/session.json", strings.NewReader("{}")))
			}
			setup.Release(t.Context())
			before := listTree(t, arcaveProjectPath(projectPath))
			var opened utils.Closers
			t.Cleanup(func() { opened.Release(context.Background()) })

			// Act
			project, err := openArcaveProjectStore(t.Context(),
				newEncryptionConfig(t, projectPath, config.Encryption{Enabled: true, Key: key}), projectPath, &opened)

			// Assert
			assert.Equal(t, before, listTree(t, arcaveProjectPath(projectPath)), "an open writes nothing")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, project.writeAdmission.AdmitWrite(t.Context()))
			after := listTree(t, arcaveProjectPath(projectPath))
			if !tt.wantMarked {
				assert.Equal(t, before, after, "a marked project is not marked again")
				return
			}
			assert.Equal(t, []string{filepath.Join(arcaveProjectPath(projectPath), "encryption.json")},
				slices.Collect(maps.Keys(after)))
		})
	}
}

// The session commands are wired with the project's write admission: on an empty encrypted
// project a listing marks nothing, and a session create marks it.
func TestOpenSessionService_WriteAdmission(t *testing.T) {
	tests := []struct {
		name       string
		run        func(ctx context.Context, svc *core.SessionService) error
		wantMarked bool
	}{
		{
			name: "a listing marks nothing",
			run: func(ctx context.Context, svc *core.SessionService) error {
				_, err := svc.ListSessions(ctx, model.ListSessionsRequest{})
				return err
			},
		},
		{
			name: "a session create marks the project",
			run: func(ctx context.Context, svc *core.SessionService) error {
				_, err := svc.CreateSession(ctx, model.CreateSessionRequest{Name: "exp"})
				return err
			},
			wantMarked: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := zerolog.Nop().WithContext(context.Background())
			projectPath := t.TempDir()
			cfg := newEncryptionConfig(t, projectPath, config.Encryption{Enabled: true, Key: newTestStoreKey(t)})
			var opened utils.Closers
			t.Cleanup(func() { opened.Release(ctx) })
			svc, err := openSessionService(ctx, projectPath, cfg, &opened)
			require.NoError(t, err)

			// Act
			err = tt.run(ctx, svc)

			// Assert
			require.NoError(t, err)
			_, statErr := os.Stat(filepath.Join(arcaveProjectPath(projectPath), "encryption.json"))
			if tt.wantMarked {
				assert.NoError(t, statErr)
			} else {
				assert.ErrorIs(t, statErr, fs.ErrNotExist)
			}
		})
	}
}

// writeAdmissionFunc is a writeAdmission over a function.
type writeAdmissionFunc func(ctx context.Context) error

func (f writeAdmissionFunc) AdmitWrite(ctx context.Context) error { return f(ctx) }

func TestFixedAdmission_AdmitWrite(t *testing.T) {
	errStore := errors.New("store unreachable")
	tests := []struct {
		name    string
		err     error
		wantErr error
		wantMsg string
	}{
		{name: "an admitted write"},
		{name: "a refusal carries its fix", err: model.ErrStoreEncrypted, wantErr: model.ErrStoreEncrypted, wantMsg: "the store is encrypted (set encryption.enabled: true and encryption.key)"},
		{name: "any other failure is returned as is", err: errStore, wantErr: errStore, wantMsg: "store unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			admission := fixedAdmission{admission: writeAdmissionFunc(func(context.Context) error { return tt.err })}

			// Act
			err := admission.AdmitWrite(t.Context())

			// Assert
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.EqualError(t, err, tt.wantMsg)
		})
	}
}

// arcaveProjectPath is where the directory storage of newEncryptionConfig keeps the demo arcave project.
func arcaveProjectPath(projectPath string) string {
	return filepath.Join(projectPath, ".arcave", "storage", "arcave", "projects", "demo")
}

// listTree is every file under root with its contents, as a refusal must leave it.
func listTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[path] = string(data)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return tree
	}
	require.NoError(t, err)
	return tree
}

func newEncryptionConfig(t *testing.T, projectPath string, encryption config.Encryption) config.Config {
	t.Helper()
	cfg := config.Config{
		Project:    config.Project{Name: "demo"},
		Encryption: encryption,
	}
	cfg.ApplyDefaults(projectPath)
	require.NoError(t, cfg.Validate())
	return cfg
}

func newTestStoreKey(t *testing.T) string {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return identity.String()
}

func readStoreObject(t *testing.T, store storages.Storager, key string) string {
	t.Helper()
	body, err := store.GetObject(t.Context(), key)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, body.Close()) })
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	return string(data)
}

// putSavepointRecord commits savepoint in store as the compose provider records one: its
// "<id>/savepoint.json".
func putSavepointRecord(ctx context.Context, t *testing.T, store storages.Storager, savepoint model.Savepoint) {
	t.Helper()
	data, err := json.Marshal(savepoint)
	require.NoError(t, err)
	require.NoError(t, store.PutObject(ctx, savepoint.ID.String()+"/savepoint.json", bytes.NewReader(data)))
}
