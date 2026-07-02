package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/greenmaskio/gm-tools/internal/client/docker"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/manifest"
	snapcopy "github.com/greenmaskio/gm-tools/internal/repository/snapshot/copy"
)

// backupArchiveName is the tar archive name used for the temporary safety
// backup taken before a rollback.
const backupArchiveName = "data.tar"

// RollbackResult reports the savepoint that was restored and which mounts it
// covered.
type RollbackResult struct {
	Savepoint model.Savepoint
	Restored  []model.Mount // Service + Target of each restored volume
}

type BackupItem struct {
	Service string
	Target  string
	Tar     string
}

func createBackup(currentMounts []model.Mount, backupAbs string, eng *docker.Engine, ctx context.Context, projectName string, progress io.Writer) ([]BackupItem, error) {
	var backup []BackupItem
	for _, mt := range currentMounts {
		tar := filepath.Join(backupAbs, mt.Service, snapcopy.MountDir(mt.Target), backupArchiveName)
		if err := os.MkdirAll(filepath.Dir(tar), 0o755); err != nil {
			return nil, fmt.Errorf("create backup directory: %w", err)
		}
		fmt.Fprintf(progress, "Backing up %s:%s...\n", mt.Service, mt.Target)
		if err := eng.ExportMount(ctx, projectName, mt.Service, mt.Target, tar); err != nil {
			return nil, err
		}
		backup = append(backup, BackupItem{mt.Service, mt.Target, tar})
	}
	return backup, nil
}

// Restore the savepoint
func restoreBackup(record manifest.Record, store *snapcopy.Store, eng *docker.Engine, ctx context.Context, projectName string, progress io.Writer) error {
	for _, v := range record.Volumes {
		fmt.Fprintf(progress, "Restoring %s:%s...\n", v.Service, v.MountPath)
		if err := eng.RestoreMount(ctx, projectName, v.Service, v.MountPath, store.Resolve(v.Snapshot)); err != nil {
			return err
		}
	}
	return nil
}

// revert restores the pre-rollback state from the safety backup. It is used
// both when the savepoint restore fails and when the restored services come
// up unhealthy. Containers may be running (started but unhealthy), so they
// are stopped first — a no-op if the restore never got far enough to start
// them.
func revert(backup []BackupItem, backupRel string, dc *docker.ComposeClient, eng *docker.Engine, ctx context.Context, projectName string, waitTimeout int, progress io.Writer, cause error) error {
	fmt.Fprintf(progress, "Rollback failed: %v\nReverting to the pre-rollback state from %s...\n", cause, backupRel)
	if err := dc.Stop(ctx); err != nil {
		return fmt.Errorf("rollback failed: %w; could not stop services to revert (backup retained at %s): %v", cause, backupRel, err)
	}
	var revertErr error
	for _, b := range backup {
		if err := eng.RestoreMount(ctx, projectName, b.Service, b.Target, b.Tar); err != nil {
			revertErr = errors.Join(revertErr, err)
		}
	}
	fmt.Fprintln(progress, "Starting services...")
	upErr := dc.Up(ctx, waitTimeout)
	switch {
	case revertErr != nil:
		return fmt.Errorf("rollback failed: %w; AUTOMATIC REVERT ALSO FAILED: %v; backup retained at %s", cause, revertErr, backupRel)
	case upErr != nil:
		return fmt.Errorf("rollback failed and was reverted to the pre-rollback state, but services failed to start: %v; backup retained at %s", upErr, backupRel)
	default:
		return fmt.Errorf("rollback failed and was reverted to the pre-rollback state: %w; backup retained at %s", cause, backupRel)
	}
}

// Rollback restores the environment to the referenced savepoint. It stops the
// services, backs up their current state under .gm/tmp so a failed restore can
// be undone, wipes and re-extracts each savepoint volume, then restarts the
// services. If any restore step fails, the pre-rollback backup is replayed to
// return the environment to where it started. waitTimeout bounds the health
// check on restart; progress is written to progress.
func (s *Service) Rollback(ctx context.Context, ref string, waitTimeout int, progress io.Writer) (RollbackResult, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return RollbackResult{}, fmt.Errorf("loading gm config file: %w", err)
	}
	mf, err := s.mf.Load()
	if err != nil {
		return RollbackResult{}, fmt.Errorf("loading manifest.json file: %w", err)
	}
	record, err := findSavepoint(mf.Savepoints, ref)
	if err != nil {
		return RollbackResult{}, fmt.Errorf("finding savepoint %q: %w", ref, err)
	}

	insp, err := s.compose.Inspect(ctx, cfg.ComposeFiles...)
	if err != nil {
		return RollbackResult{}, fmt.Errorf("inspecting compose files: %w", err)
	}
	currentMounts := insp.Mounts
	projectName := insp.ProjectName

	store := snapcopy.New(s.projectRoot, snapshotsDir(cfg))

	// Pre-flight: every savepoint archive must exist before we touch anything.
	for _, v := range record.Volumes {
		if _, err := os.Stat(store.Resolve(v.Snapshot)); err != nil {
			return RollbackResult{}, fmt.Errorf("savepoint archive missing for %s %s: %w", v.Service, v.MountPath, err)
		}
	}

	dc := docker.NewComposeClient(cfg.ComposeFiles...)
	eng, err := docker.NewEngine()
	if err != nil {
		return RollbackResult{}, fmt.Errorf("creating docker engine: %w", err)
	}

	defer func() {
		if err := eng.Close(); err != nil {
			fmt.Fprintf(progress, "warning: failed to close docker engine: %v\n", err)
		}
	}()

	fmt.Fprintln(progress, "Stopping services...")
	if err := dc.Stop(ctx); err != nil {
		return RollbackResult{}, fmt.Errorf("stopping services: %w", err)
	}

	// Safety backup of the current live state, so a failed restore can be undone.
	backupRel := filepath.Join(".gm", "tmp", "rollback-"+strconv.FormatInt(time.Now().Unix(), 10))
	backupAbs := s.abs(backupRel)
	backup, err := createBackup(currentMounts, backupAbs, eng, ctx, projectName, progress)
	if err != nil {
		os.RemoveAll(backupAbs)
		fmt.Fprintln(progress, "Starting services...")
		if err := dc.Up(ctx, waitTimeout); err != nil {
			return RollbackResult{}, fmt.Errorf("pre-rollback backup failed: %w; additionally failed to restart services: %v", err, err)
		}
		return RollbackResult{}, fmt.Errorf("pre-rollback backup failed, nothing changed: %w", err)
	}

	// Restore the savepoint.
	err = restoreBackup(record, store, eng, ctx, projectName, progress)
	if err != nil {
		return RollbackResult{}, revert(backup, backupRel, dc, eng, ctx, projectName, waitTimeout, progress, err)
	}

	// Bring services up and verify health. An unhealthy start (up -d --wait
	// fails) reverts to the pre-rollback state just like a restore failure.
	fmt.Fprintln(progress, "Starting services...")
	if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
		return RollbackResult{}, revert(backup, backupRel, dc, eng, ctx, projectName, waitTimeout, progress, fmt.Errorf("services did not become healthy after restore: %w", upErr))
	}

	// The rollback completed and services are healthy; drop the safety backup.
	os.RemoveAll(backupAbs)
	os.Remove(s.abs(filepath.Join(".gm", "tmp"))) // drop the parent if now empty

	restored := make([]model.Mount, 0, len(record.Volumes))
	for _, v := range record.Volumes {
		restored = append(restored, model.Mount{Service: v.Service, Target: v.MountPath})
	}
	return RollbackResult{Savepoint: record.Savepoint, Restored: restored}, nil
}
