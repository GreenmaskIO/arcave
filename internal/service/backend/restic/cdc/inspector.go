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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// totalPrunePattern is the line of a prune dry run naming what it would remove, as restic
// 0.19.1 prints it: prune has no --json output, and forget --json suppresses it.
var totalPrunePattern = regexp.MustCompile(`^total prune:\s+\d+ blobs / ([0-9.]+) (B|KiB|MiB|GiB|TiB)$`)

// pruneUnits are the binary units restic formats a size in.
var pruneUnits = map[string]float64{
	"B":   1,
	"KiB": 1 << 10,
	"MiB": 1 << 20,
	"GiB": 1 << 30,
	"TiB": 1 << 40,
}

// Snapshot is one restic snapshot of a repository.
type Snapshot struct {
	ID string
	// Savepoint is the savepoint whose capture took it; zero for a snapshot arcave did not tag.
	Savepoint model.SavepointID
	Time      time.Time
}

// resticSnapshot is one entry of snapshots --json.
type resticSnapshot struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Tags []string  `json:"tags"`
}

// Inspector reads repositories through restic, read-only: it takes no restic lock and clears none,
// since a backup on another host may hold one.
type Inspector struct {
	runner      ContainerRunner
	repoURLBase string
	options     Options
}

// NewInspector runs restic through runner against the repositories repoURLBase addresses.
func NewInspector(runner ContainerRunner, repoURLBase string, options Options) *Inspector {
	return &Inspector{
		runner:      runner,
		repoURLBase: repoURLBase,
		options:     options,
	}
}

// ReadSnapshots lists the snapshots of objectID's repository.
func (s *Inspector) ReadSnapshots(ctx context.Context, objectID string) ([]Snapshot, error) {
	out, err := s.newRunner(objectID).output(ctx, resticCmd{args: []string{"snapshots", "--json", "--no-lock"}})
	if err != nil {
		return nil, err
	}
	return parseSnapshots(out)
}

// EstimateForget is what forgetting ids, then pruning, would free in objectID's repository; nil
// when restic printed no estimate.
func (s *Inspector) EstimateForget(ctx context.Context, objectID string, ids []string) (*int64, error) {
	for _, id := range ids {
		// An id is an argument: one that is not restic's could read as a flag.
		if !isSnapshotID(id) {
			return nil, fmt.Errorf("snapshot id %q is not a full restic id", id)
		}
	}
	args := append([]string{"forget"}, ids...)
	args = append(args, "--prune", "--dry-run", "--no-lock")
	out, err := s.newRunner(objectID).output(ctx, resticCmd{args: args})
	if err != nil {
		return nil, err
	}
	freed, found, err := parsePruneEstimate(out)
	if err != nil {
		return nil, err
	}
	if !found {
		zerolog.Ctx(ctx).Warn().Str("repository", objectID).Msg("restic printed no prune estimate")
		return nil, nil
	}
	return &freed, nil
}

// ListRepositories names the repositories of a compose project store, one per volume's ObjectID.
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
		repos = append(repos, dir.Dirname())
	}
	return repos, nil
}

// newRunner is a runner to objectID's repository, without clearing its locks.
func (s *Inspector) newRunner(objectID string) runner {
	return runner{
		containers:  s.runner,
		image:       s.options.Image,
		repoURL:     newRepositoryURL(s.repoURLBase, objectID),
		hostNetwork: s.options.HostNetwork,
	}
}

// parseSnapshots reads snapshots --json, an array, into snapshots.
func parseSnapshots(out []byte) ([]Snapshot, error) {
	var entries []resticSnapshot
	if err := json.Unmarshal(bytes.TrimSpace(out), &entries); err != nil {
		return nil, fmt.Errorf("decode restic snapshots: %w", err)
	}
	snapshots := make([]Snapshot, 0, len(entries))
	for _, entry := range entries {
		snapshots = append(snapshots, Snapshot{
			ID:        entry.ID,
			Savepoint: parseSavepointTag(entry.Tags),
			Time:      entry.Time,
		})
	}
	return snapshots, nil
}

// parseSavepointTag is the savepoint the savepointTag of tags names; zero when none does.
func parseSavepointTag(tags []string) model.SavepointID {
	for _, tag := range tags {
		value, ok := strings.CutPrefix(tag, savepointTag+"=")
		if id := model.SavepointID(value); ok && id.Validate() == nil {
			return id
		}
	}
	return ""
}

// isSnapshotID reports whether id is a full restic id: 64 lowercase hex digits.
func isSnapshotID(id string) bool {
	if len(id) != 64 {
		return false
	}
	return strings.Trim(id, "0123456789abcdef") == ""
}

// parsePruneEstimate reads the bytes a prune dry run's total line names, rounded as restic
// printed them; found is false when out holds no such line.
func parsePruneEstimate(out []byte) (freed int64, found bool, err error) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		match := totalPrunePattern.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if match == nil {
			continue
		}
		size, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, false, fmt.Errorf("parse prune estimate %q: %w", match[0], err)
		}
		return int64(math.Round(size * pruneUnits[match[2]])), true, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, false, fmt.Errorf("read prune output: %w", err)
	}
	return 0, false, nil
}
