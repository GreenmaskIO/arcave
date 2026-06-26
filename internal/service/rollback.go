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

// Rollback restores the environment to the referenced savepoint. It stops the
// services, backs up their current state under .gm/tmp so a failed restore can
// be undone, wipes and re-extracts each savepoint volume, then restarts the
// services. If any restore step fails, the pre-rollback backup is replayed to
// return the environment to where it started. waitTimeout bounds the health
// check on restart; progress is written to progress.
func (s *Service) Rollback(ctx context.Context, ref string, waitTimeout int, progress io.Writer) (RollbackResult, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return RollbackResult{}, err
	}
	mf, err := s.mf.Load()
	if err != nil {
		return RollbackResult{}, err
	}
	record, err := findSavepoint(mf.Savepoints, ref)
	if err != nil {
		return RollbackResult{}, err
	}

	insp, err := s.compose.Inspect(ctx, cfg.ComposeFiles...)
	if err != nil {
		return RollbackResult{}, err
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
		return RollbackResult{}, err
	}
	defer eng.Close()

	fmt.Fprintln(progress, "Stopping services...")
	if err := dc.Stop(ctx); err != nil {
		return RollbackResult{}, err
	}

	// Safety backup of the current live state, so a failed restore can be undone.
	backupRel := filepath.Join(".gm", "tmp", "rollback-"+strconv.FormatInt(time.Now().Unix(), 10))
	backupAbs := s.abs(backupRel)
	type backupItem struct{ service, target, tar string }
	var backup []backupItem
	backupErr := func() error {
		for _, mt := range currentMounts {
			tar := filepath.Join(backupAbs, mt.Service, snapcopy.MountDir(mt.Target), backupArchiveName)
			if err := os.MkdirAll(filepath.Dir(tar), 0o755); err != nil {
				return fmt.Errorf("create backup directory: %w", err)
			}
			fmt.Fprintf(progress, "Backing up %s:%s...\n", mt.Service, mt.Target)
			if err := eng.ExportMount(ctx, projectName, mt.Service, mt.Target, tar); err != nil {
				return err
			}
			backup = append(backup, backupItem{mt.Service, mt.Target, tar})
		}
		return nil
	}()
	if backupErr != nil {
		os.RemoveAll(backupAbs)
		fmt.Fprintln(progress, "Starting services...")
		if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
			return RollbackResult{}, fmt.Errorf("pre-rollback backup failed: %w; additionally failed to restart services: %v", backupErr, upErr)
		}
		return RollbackResult{}, fmt.Errorf("pre-rollback backup failed, nothing changed: %w", backupErr)
	}

	// revert restores the pre-rollback state from the safety backup. It is used
	// both when the savepoint restore fails and when the restored services come
	// up unhealthy. Containers may be running (started but unhealthy), so they
	// are stopped first — a no-op if the restore never got far enough to start
	// them.
	revert := func(cause error) error {
		fmt.Fprintf(progress, "Rollback failed: %v\nReverting to the pre-rollback state from %s...\n", cause, backupRel)
		if err := dc.Stop(ctx); err != nil {
			return fmt.Errorf("rollback failed: %w; could not stop services to revert (backup retained at %s): %v", cause, backupRel, err)
		}
		var revertErr error
		for _, b := range backup {
			if err := eng.RestoreMount(ctx, projectName, b.service, b.target, b.tar); err != nil {
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

	// Restore the savepoint.
	restoreErr := func() error {
		for _, v := range record.Volumes {
			fmt.Fprintf(progress, "Restoring %s:%s...\n", v.Service, v.MountPath)
			if err := eng.RestoreMount(ctx, projectName, v.Service, v.MountPath, store.Resolve(v.Snapshot)); err != nil {
				return err
			}
		}
		return nil
	}()
	if restoreErr != nil {
		return RollbackResult{}, revert(restoreErr)
	}

	// Bring services up and verify health. An unhealthy start (up -d --wait
	// fails) reverts to the pre-rollback state just like a restore failure.
	fmt.Fprintln(progress, "Starting services...")
	if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
		return RollbackResult{}, revert(fmt.Errorf("services did not become healthy after restore: %w", upErr))
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
