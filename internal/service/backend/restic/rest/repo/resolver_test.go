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

package repo

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/greenmaskio/storages"
	"github.com/greenmaskio/storages/directory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// A name is a directory under the root, so what Lookup answers with has to write
// exactly there — one level down, never beside the root and never over another
// repository's tree.
func TestResolver_Lookup(t *testing.T) {
	// Arrange
	root := t.TempDir()
	store, err := directory.NewStorage(directory.Config{RootPath: root})
	require.NoError(t, err)
	resolver := NewResolver(storages.Guard(store))

	tests := []struct {
		name           string
		arcaveObjectID string
	}{
		{name: "a named volume", arcaveObjectID: "volume-named-demo_pgdata"},
		{name: "a service mount", arcaveObjectID: "volume-service-app-var-lib-data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			repo, err := resolver.Lookup(tt.arcaveObjectID)

			// Assert
			require.NoError(t, err)
			require.NoError(t, repo.SaveConfig(context.Background(), strings.NewReader("a config")))
			assert.FileExists(t, filepath.Join(root, tt.arcaveObjectID, "config"))
		})
	}
}

// Two names are two repositories: a config written through one is invisible to
// the other, which is the whole of what "a repository per volume" rests on.
func TestResolver_Lookup_KeepsRepositoriesApart(t *testing.T) {
	// Arrange
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	resolver := NewResolver(storages.Guard(store))

	first, err := resolver.Lookup("volume-named-first")
	require.NoError(t, err)
	second, err := resolver.Lookup("volume-named-second")
	require.NoError(t, err)

	// Act
	require.NoError(t, first.SaveConfig(context.Background(), strings.NewReader("a config")))

	// Assert
	_, err = second.StatConfig(context.Background())
	assert.ErrorIs(t, err, model.ErrObjectNotFound)
}

// The root is the boundary: a name that is not one segment inside it never
// resolves, so nothing above has to sanitise what arrives on the wire. The
// traversals are refused by the store's own guard, the other two by shape.
func TestResolver_Lookup_RefusesWhatCannotNameARepository(t *testing.T) {
	// Arrange
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	resolver := NewResolver(storages.Guard(store))

	tests := []struct {
		name           string
		arcaveObjectID string
	}{
		{name: "the root itself", arcaveObjectID: ""},
		{name: "more than one segment", arcaveObjectID: "volume/demo_pgdata"},
		{name: "a traversal", arcaveObjectID: ".."},
		{name: "a traversal out of the root", arcaveObjectID: "../elsewhere"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			repo, err := resolver.Lookup(tt.arcaveObjectID)

			// Assert
			assert.ErrorIs(t, err, model.ErrRepositoryNotFound)
			assert.Nil(t, repo)
		})
	}
}

// The resolver holds no state of its own, and the server resolves per request:
// run under -race, this is what says so.
func TestResolver_Lookup_Concurrent(t *testing.T) {
	// Arrange
	store, err := directory.NewStorage(directory.Config{RootPath: t.TempDir()})
	require.NoError(t, err)
	resolver := NewResolver(storages.Guard(store))

	var wg sync.WaitGroup

	// Act
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo, err := resolver.Lookup("volume-named-demo_pgdata")
			assert.NoError(t, err)
			assert.NotNil(t, repo)
		}()
	}
	wg.Wait()
}
