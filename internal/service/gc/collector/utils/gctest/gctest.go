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

// Package gctest holds the fixtures the collectors' tests share: a directory store, the objects
// planted in it and the scope gc would build over it.
package gctest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/gc"
)

const (
	LiveSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000001")
	DeadSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000002")
	// OtherDeadSavepoint is another recordless tree.
	OtherDeadSavepoint = model.SavepointID("019e843e-1650-7000-8000-000000000004")
)

var (
	Now    = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	Old    = Now.Add(-48 * time.Hour)
	Recent = Now.Add(-time.Hour)

	LiveBlob = strings.Repeat("a", 64)
	DeadBlob = strings.Repeat("b", 64)
	NewBlob  = strings.Repeat("c", 64)

	LiveImage = Digest("1")
	DeadImage = Digest("2")
)

// NewStore returns a directory store over a fresh temp dir, and that dir.
func NewStore(t *testing.T) (storages.Storager, string) {
	t.Helper()
	root := t.TempDir()
	store, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	return store, root
}

// WriteObject plants body at key and sets its modification time.
func WriteObject(t *testing.T, root, key, body string, modified time.Time) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	require.NoError(t, os.Chtimes(full, modified, modified))
}

// WriteJSON plants value at key, as JSON.
func WriteJSON(t *testing.T, root, key string, value any, modified time.Time) {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	WriteObject(t, root, key, string(body), modified)
}

// NewScope is the scope gc builds over store: listed once, as the store holds it now.
func NewScope(t *testing.T, store storages.Storager, savepoints ...model.Savepoint) gc.ComposeProjectScope {
	t.Helper()
	objects, err := store.List(context.Background(), "")
	require.NoError(t, err)
	return gc.ComposeProjectScope{
		Store:      store,
		Objects:    objects,
		Savepoints: savepoints,
	}
}

// Reasons lists each skip's reason.
func Reasons(skips []model.GarbageSkip) []model.GarbageReason {
	out := make([]model.GarbageReason, 0, len(skips))
	for _, skip := range skips {
		out = append(out, skip.Reason)
	}
	return out
}

// Digest is the image id of 64 chars.
func Digest(char string) model.ImageDigestID {
	return model.ImageDigestID("sha256:" + strings.Repeat(char, 64))
}

// Targets lists each op's target.
func Targets(ops []model.GarbageOp) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.Target)
	}
	return out
}

// SkippedTargets lists each skip's target.
func SkippedTargets(skips []model.GarbageSkip) []string {
	out := make([]string, 0, len(skips))
	for _, skip := range skips {
		out = append(out, skip.Target)
	}
	return out
}

// NilIfEmpty is values, or nil when empty.
func NilIfEmpty[T any](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	return values
}
