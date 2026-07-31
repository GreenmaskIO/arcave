package cdc

import (
	"context"
	"fmt"
	"sort"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	resticmodel "github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// savepointTag ties a restic snapshot back to the savepoint that took it — the
// only way to read the repository without arcave's own record.
const savepointTag = "arcave-savepoint"

// Snapshotter captures one volume into one restic repository. Its tier shows up
// in the publish prefix and the capture's labels, never in the repository: both
// tiers write to the same one.
type Snapshotter struct {
	volume model.Volume
	mount  captureMountRef
	// repository is what a restic container reaches over the protocol; admin is
	// the same repository addressed as objects, without a container.
	repository resticmodel.Repository
	admin      repoAdmin
	prefix     string
	// mounted is where the volume sits inside the restic container, and therefore
	// the path inside every snapshot.
	mounted string
	deps    Deps
}

func (s *Snapshotter) Volume() model.Volume { return s.volume }

// Snapshot backs the volume up, initialising the repository on the first
// capture. The volume is mounted read-only.
func (s *Snapshotter) Snapshot(
	ctx context.Context, req model.BackendSnapshotRequest,
) (model.SnapshotStat, error) {
	if err := req.SnapshotID.Validate(); err != nil {
		return model.SnapshotStat{}, err
	}

	source, err := s.bindSource(ctx)
	if err != nil {
		return model.SnapshotStat{}, err
	}

	run, unpublish, err := s.open(ctx)
	if err != nil {
		return model.SnapshotStat{}, err
	}
	defer unpublish()

	initialised, err := s.admin.HasConfig(ctx)
	if err != nil {
		return model.SnapshotStat{}, err
	}
	if !initialised {
		zerolog.Ctx(ctx).Info().Msg("initialising the restic repository")
		if _, err := run.run(ctx, resticCmd{args: []string{"init", "--json"}}); err != nil {
			return model.SnapshotStat{}, err
		}
	}

	summary, err := run.run(ctx, resticCmd{
		args:  s.backupArgs(req),
		binds: []string{source + ":" + s.mounted + ":ro"},
	})
	if err != nil {
		return model.SnapshotStat{}, err
	}
	if err := validateSnapshotRef(summary.SnapshotID); err != nil {
		return model.SnapshotStat{}, fmt.Errorf("restic backup reported no usable snapshot id: %w", err)
	}

	return model.SnapshotStat{
		Volume: s.volume,
		Type:   model.SnapshotBackendCDC,
		Payload: Payload{
			SnapshotRef:         summary.SnapshotID,
			DataAdded:           summary.DataAdded,
			TotalBytesProcessed: summary.TotalBytesProcessed,
		},
	}, nil
}

// backupArgs builds the backup command line. --host and the path are the same
// for both tiers on purpose: restic picks a parent snapshot by them, and a
// safety backup that missed the parent would reship the whole volume.
func (s *Snapshotter) backupArgs(req model.BackendSnapshotRequest) []string {
	args := []string{
		"backup", s.mounted,
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
	payload, err := decodePayload(req.Payload)
	if err != nil {
		return err
	}
	if err := validateSnapshotRef(payload.SnapshotRef); err != nil {
		return err
	}

	source, err := s.bindSource(ctx)
	if err != nil {
		return err
	}

	// The wipe must finish before restic writes a byte; it is a container of its
	// own and needs no repository.
	if !s.deps.Options.Incremental {
		if err := s.deps.Runner.WipeMount(ctx, s.volume.ComposeProject, s.mount.service, s.mount.target); err != nil {
			return fmt.Errorf("clear the mount before restore: %w", err)
		}
	}

	run, unpublish, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unpublish()

	args := []string{"restore", payload.SnapshotRef, "--target", "/", "--include", s.mounted}
	if s.deps.Options.Incremental {
		// Needs restic ≥0.17, which compares content rather than size+mtime.
		args = append(args, "--delete", "--overwrite", s.deps.Options.Overwrite)
	}
	_, err = run.run(ctx, resticCmd{
		args:  append(args, "--json"),
		binds: []string{source + ":" + s.mounted},
	})
	return err
}

// Exists answers true without asking the repository: the savepoint record is
// written last, so a record naming this capture already states that it
// completed. The blind spot is a snapshot deleted behind arcave's back, which
// surfaces mid-rollback and rides the ordinary revert.
func (*Snapshotter) Exists(context.Context, model.BackendExistsRequest) (bool, error) {
	return true, nil
}

// Remove drops the capture's snapshot, server-side. A capture that reported no
// payload has nothing to drop — the ordinary cleanup path of a failed create.
func (s *Snapshotter) Remove(ctx context.Context, req model.BackendRemoveRequest) error {
	if err := req.SnapshotID.Validate(); err != nil {
		return err
	}
	payload, err := decodePayload(req.Payload)
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("nothing to remove: the capture reported no snapshot")
		return nil
	}
	if payload.SnapshotRef == "" {
		return nil
	}
	return s.admin.DeleteSnapshot(ctx, payload.SnapshotRef)
}

// open publishes the repository for the length of one operation, clears whatever
// locks an earlier run orphaned, and returns the runner bound to the URL a
// container reaches it at. The unpublish is the caller's to defer.
//
// The registration lives for one operation rather than for the snapshotter: the
// server listens for the whole process, but a repository is reachable only while
// its own operation runs. A request already holding it plays out.
func (s *Snapshotter) open(ctx context.Context) (runner, func(), error) {
	if err := s.deps.Publisher.Register(s.prefix, s.repository); err != nil {
		return runner{}, nil, fmt.Errorf("publish the %s repository: %w", s.volume.ObjectID, err)
	}
	unpublish := func() { s.deps.Publisher.Unregister(s.prefix) }
	if err := s.admin.ClearLocks(ctx); err != nil {
		unpublish()
		return runner{}, nil, err
	}
	return runner{
		containers: s.deps.Runner,
		image:      s.deps.Options.Image,
		repoURL:    s.repositoryURL(),
	}, unpublish, nil
}

// repositoryURL addresses this instance's repository on the data plane: the base
// the DI root resolved, plus the prefix it publishes under. The trailing slash
// is the restic protocol's, and losing it fails quietly — restic keeps talking,
// to the wrong path.
func (s *Snapshotter) repositoryURL() string {
	return s.deps.RepoURLBase + "/" + s.prefix + "/"
}

// bindSource resolves what the restic container binds to see the volume: the
// docker volume name, or the host path of a bind mount.
func (s *Snapshotter) bindSource(ctx context.Context) (string, error) {
	source, err := s.deps.Runner.ResolveMountSource(ctx, s.volume.ComposeProject, s.mount.service, s.mount.target)
	if err != nil {
		return "", fmt.Errorf("resolve %s mount %s: %w", s.mount.service, s.mount.target, err)
	}
	return source, nil
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
