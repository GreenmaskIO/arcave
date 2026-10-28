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
	"sort"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// savepointTag ties a restic snapshot back to the savepoint that took it — the
// only way to read the repository without arcave's own record.
const savepointTag = "arcave-savepoint"

// Snapshotter captures one volume into one restic repository. A savepoint and a
// rollback's undo of the same volume write to that one repository, and are told
// apart by the labels their captures carry — nothing else.
//
// It binds the volume into a container of its own, so no service container is
// involved and the volume is always captured whole.
type Snapshotter struct {
	volume model.Volume
	// repo is this instance's repository addressed as objects, without a
	// container. What a restic container reaches over the protocol is the same
	// repository, resolved by the server from the volume's ObjectID.
	repo objectRepo
	// mountPath is where the volume sits inside the restic container, and therefore
	// the path inside every snapshot.
	mountPath string
	deps      Dependencies
}

func (s *Snapshotter) Volume() model.Volume { return s.volume }

// Snapshot backs the volume up, initialising the repository on the first
// capture. The volume is bound whole and mounted read-only.
func (s *Snapshotter) Snapshot(
	ctx context.Context, req model.BackendSnapshotRequest,
) (model.SnapshotStat, error) {
	if err := req.SnapshotID.Validate(); err != nil {
		return model.SnapshotStat{}, err
	}

	src, err := s.mountSource(ctx)
	if err != nil {
		return model.SnapshotStat{}, err
	}

	resticRunner, err := s.openResticRunner(ctx)
	if err != nil {
		return model.SnapshotStat{}, fmt.Errorf("opening snapshot: %w", err)
	}

	initialised, err := s.repo.HasConfig(ctx)
	if err != nil {
		return model.SnapshotStat{}, err
	}
	if !initialised {
		zerolog.Ctx(ctx).Info().Msg("initialising the restic repository")
		if _, err := resticRunner.run(ctx, resticCmd{args: []string{"init", "--json"}}); err != nil {
			return model.SnapshotStat{}, err
		}
	}

	summary, err := resticRunner.run(ctx, resticCmd{
		args:  s.newBackupArgs(req),
		binds: []string{src.Source + ":" + s.mountPath + ":ro"},
	})
	if err != nil {
		return model.SnapshotStat{}, err
	}
	// The one thing worth checking about the id: restic printed one. An empty id
	// would be committed into the record and only surface at the rollback that
	// cannot find its snapshot.
	if summary.SnapshotID == "" {
		return model.SnapshotStat{}, errNoSnapshotID
	}

	return model.SnapshotStat{
		Volume:      s.volume,
		Type:        model.SnapshotBackendCDC,
		SizeBytes:   summary.TotalBytesProcessed,
		StoredBytes: summary.DataAddedPacked,
		Payload:     Payload{SnapshotObjectID: summary.SnapshotID},
	}, nil
}

// newBackupArgs builds the backup command line. --host and the path do not depend
// on what the capture is for, on purpose: restic picks a parent snapshot by
// them, and a safety snapshot that missed the parent would reship the whole
// volume. What it is for shows up in the labels the caller passed, and nowhere
// else.
func (s *Snapshotter) newBackupArgs(req model.BackendSnapshotRequest) []string {
	args := []string{
		"backup", s.mountPath,
		"--host", s.volume.ComposeProject,
		"--tag", savepointTag + "=" + req.SnapshotID.String(),
	}
	for _, key := range sortedKeys(req.Labels) {
		args = append(args, "--tag", key+"="+req.Labels[key])
	}
	return append(args, "--json")
}

// Restore writes the snapshot the payload names back into the volume, in the
// mode the project configured.
//
// The target is / rather than the mount: snapshot paths are absolute
// (/m/<object-id>), so restoring at the root lands them in the mounted volume.
// --include keeps that scoped, and is required for --delete not to aim at the
// container's whole filesystem.
func (s *Snapshotter) Restore(ctx context.Context, req model.BackendRestoreRequest) error {
	if err := req.SnapshotID.Validate(); err != nil {
		return err
	}
	payload, err := DecodePayload(req.Payload)
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	// A record naming no snapshot is refused ahead of the wipe: there is nothing
	// to restore, so clearing the volume would destroy live data for nothing. An
	// id that is present but restic cannot resolve is not arcave's to judge — it
	// fails as R5 does, after the wipe, and rides the ordinary revert.
	if payload.SnapshotObjectID == "" {
		return errNoSnapshotID
	}

	src, err := s.mountSource(ctx)
	if err != nil {
		return fmt.Errorf("mount source: %w", err)
	}

	// The wipe must finish before restic writes a byte; it is a container of its
	// own and needs no repository.
	if !s.deps.Options.Incremental {
		if err := s.deps.Runner.WipeVolume(ctx, src); err != nil {
			return fmt.Errorf("clear the volume before restore: %w", err)
		}
	}

	resticRunner, err := s.openResticRunner(ctx)
	if err != nil {
		return fmt.Errorf("open restic runner: %w", err)
	}

	args := []string{"restore", payload.SnapshotObjectID, "--target", "/", "--include", s.mountPath}
	if s.deps.Options.Incremental {
		// Needs restic ≥0.17, which compares content rather than size+mtime.
		args = append(args, "--delete", "--overwrite", s.deps.Options.Overwrite)
	}
	_, err = resticRunner.run(ctx, resticCmd{
		args:  append(args, "--json"),
		binds: []string{src.Source + ":" + s.mountPath},
	})
	return err
}

// Remove leaves the capture's snapshot in place: tagged with a savepoint no record commits, it is
// what arcave gc forgets, pruning the data it alone held.
func (s *Snapshotter) Remove(_ context.Context, req model.BackendRemoveRequest) error {
	return req.SnapshotID.Validate()
}

// openResticRunner clears whatever locks an earlier run orphaned and returns the runner
// bound to the URL a container reaches the repository at.
//
// Only at the start: restic refreshes its own lock while it works, and those
// writes are ordinary traffic. Every standing lock here is an orphan — a lock
// written inside a container carries that container's hostname, so restic in the
// next one reads it as a stranger's and `unlock` will never pick it — and it is
// safe to drop because arcave holds the project lock and this repository has no
// other client by construction.
func (s *Snapshotter) openResticRunner(ctx context.Context) (runner, error) {
	if err := s.repo.ClearLocks(ctx); err != nil {
		return runner{}, err
	}
	return s.newRunner()
}

// newRunner is the runner to this instance's repository, without clearing its locks.
func (s *Snapshotter) newRunner() (runner, error) {
	return newRepositories(s.deps.Runner, s.deps.RepoURLBase, s.deps.Options).newRunner(s.volume.ObjectID)
}

// mountSource resolves what the restic container mounts to see the volume.
// Resolved per operation rather than held on the instance: it may cost a round
// trip to the daemon, and only the two operations that move bytes need it.
//
// The volume is mounted whole, whatever subpath a service happens to mount — so
// a capture holds all of it, and a restore puts all of it back.
func (s *Snapshotter) mountSource(ctx context.Context) (model.MountSource, error) {
	src, err := s.deps.Runner.ResolveMountSource(ctx, s.volume)
	if err != nil {
		return model.MountSource{}, fmt.Errorf("resolve the mount source of volume %s: %w", s.volume.ObjectID, err)
	}
	return src, nil
}

// sortedKeys orders a label set, so the command line is stable across runs.
func sortedKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
