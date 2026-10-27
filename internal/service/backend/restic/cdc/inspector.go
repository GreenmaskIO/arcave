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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
)

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
	repositories repositories
}

// NewInspector runs restic through runner against the repositories repoURLBase addresses.
func NewInspector(runner ContainerRunner, repoURLBase string, options Options) *Inspector {
	return &Inspector{repositories: newRepositories(runner, repoURLBase, options)}
}

// ReadSnapshots lists the snapshots of objectID's repository.
func (s *Inspector) ReadSnapshots(ctx context.Context, objectID string) ([]Snapshot, error) {
	resticRunner, err := s.repositories.newRunner(objectID)
	if err != nil {
		return nil, err
	}
	out, err := resticRunner.output(ctx, resticCmd{args: []string{"snapshots", "--json", "--no-lock"}})
	if err != nil {
		return nil, err
	}
	return parseSnapshots(out)
}

// EstimateForget is what forgetting ids, then pruning, would free in objectID's repository, what
// a prune alone would when ids is empty; nil when restic printed no estimate.
func (s *Inspector) EstimateForget(ctx context.Context, objectID string, ids []string) (*int64, error) {
	args, err := newPruneArgs(ids, "--dry-run", "--no-lock")
	if err != nil {
		return nil, err
	}
	resticRunner, err := s.repositories.newRunner(objectID)
	if err != nil {
		return nil, err
	}
	out, err := resticRunner.output(ctx, resticCmd{args: args})
	if err != nil {
		return nil, err
	}
	return readPruneTotal(ctx, out, objectID)
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
