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

package cdc

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every question about a repository's state is an object question, answered
// without a container and without the repository password.

func TestRepoAdmin_HasConfig(t *testing.T) {
	tests := []struct {
		name string
		// plant is what the repository already holds.
		plant map[string]string
		want  bool
	}{
		{name: "an absent repository has never been initialised"},
		{
			name:  "a config object is the mark of an initialised one",
			plant: map[string]string{"config": "restic"},
			want:  true,
		},
		{
			name:  "objects without a config do not count",
			plant: map[string]string{"data/00/abc": "pack"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := objectRepo{store: newStore(t)}
			plant(t, repo, tt.plant)

			// Act
			got, err := repo.HasConfig(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A lock written inside a container carries that container's hostname, so
// `restic unlock` from the next one can never clear it. arcave holds the project
// flock for the whole workflow and the repository has no other client, so every
// lock standing at the start of an operation is an orphan by construction.
func TestRepoAdmin_ClearLocks(t *testing.T) {
	tests := []struct {
		name  string
		plant map[string]string
		// wantKept is the whole tree afterwards.
		wantKept []string
	}{
		{
			name:     "an unlocked repository is left alone",
			plant:    map[string]string{"config": "restic"},
			wantKept: []string{"config"},
		},
		{
			name: "every standing lock goes, and nothing else does",
			plant: map[string]string{
				"config":         "restic",
				"locks/abc123":   "orphan",
				"locks/def456":   "orphan",
				"snapshots/ff00": "a capture",
			},
			wantKept: []string{"config", "snapshots/ff00"},
		},
		{
			name:     "a repository that does not exist yet has no locks to clear",
			wantKept: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := newStore(t)
			repo := objectRepo{store: store}
			plant(t, repo, tt.plant)

			// Act
			err := repo.ClearLocks(context.Background())

			// Assert
			require.NoError(t, err)
			assert.Empty(t, objectsUnder(t, store, locksPrefix))
			assert.ElementsMatch(t, tt.wantKept, objectsUnder(t, store, ""))
		})
	}
}

// plant writes the objects a repository is supposed to already hold.
func plant(t *testing.T, repo objectRepo, objects map[string]string) {
	t.Helper()

	for key, body := range objects {
		require.NoError(t, repo.store.PutObject(context.Background(), key, strings.NewReader(body)))
	}
}
