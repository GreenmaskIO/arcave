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
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"
)

var (
	// repositoryPattern is the allowlist of a repository's segment: a volume's ObjectID, one
	// segment opening with "volume-", whatever the mount target flattened into it holds.
	repositoryPattern = regexp.MustCompile(`^volume-[^/]+$`)
	// snapshotIDPattern is a full restic id.
	snapshotIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ListRepositories names the repositories of a compose project store, one per volume's ObjectID; a
// directory that is no volume's is warned of and left out.
func ListRepositories(ctx context.Context, store storages.Navigator) ([]string, error) {
	root, err := store.SubStorage(NamespaceRepos, true)
	if err != nil {
		return nil, fmt.Errorf("re-root store at %s: %w", NamespaceRepos, err)
	}
	_, dirs, err := root.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", NamespaceRepos, err)
	}
	repos := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if err := requireRepository(dir.Dirname()); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("skipping a directory of the restic repositories")
			continue
		}
		repos = append(repos, dir.Dirname())
	}
	return repos, nil
}

// NewRepositoryKey is the key of objectID's repository in a compose project store.
func NewRepositoryKey(objectID string) string {
	return path.Join(NamespaceRepos, objectID)
}

// NewSnapshotKey is the key of snapshot id of objectID's repository in a compose project store.
func NewSnapshotKey(objectID, id string) string {
	return path.Join(NamespaceRepos, objectID, snapshotsPath, id)
}

// ParseSnapshotKey splits a key NewSnapshotKey built into its repository and snapshot id.
func ParseSnapshotKey(key string) (objectID, id string, err error) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != NamespaceRepos || parts[2] != snapshotsPath {
		return "", "", fmt.Errorf("%q is no restic snapshot key", key)
	}
	if err := requireRepository(parts[1]); err != nil {
		return "", "", fmt.Errorf("snapshot key %q: %w", key, err)
	}
	if err := requireSnapshotID(parts[3]); err != nil {
		return "", "", fmt.Errorf("snapshot key %q: %w", key, err)
	}
	return parts[1], parts[3], nil
}

// requireRepository refuses an objectID that is not a volume's: it becomes a path segment.
func requireRepository(objectID string) error {
	if !repositoryPattern.MatchString(objectID) {
		return fmt.Errorf("%q is no volume's repository", objectID)
	}
	return nil
}

// requireSnapshotID refuses an id that is not a full restic id: an id is an argument, and one that
// is not restic's could read as a flag.
func requireSnapshotID(id string) error {
	if !snapshotIDPattern.MatchString(id) {
		return fmt.Errorf("snapshot id %q is not a full restic id", id)
	}
	return nil
}
