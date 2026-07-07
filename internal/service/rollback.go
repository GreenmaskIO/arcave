package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/greenmaskio/gm-tools/internal/config"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/backup"
	"github.com/greenmaskio/gm-tools/internal/repository/snapshot"
	"github.com/greenmaskio/gm-tools/internal/volume"
)

// maxConcurrentVolumeOperations caps how many mount export/restore operations
// run in parallel, bounding pressure on Docker and the host filesystem.
const maxConcurrentVolumeOperations = 5

// RollbackResult reports the savepoint that was restored and which mounts it
// covered.
type RollbackResult struct {
	Savepoint model.Savepoint
	Restored  []model.Mount // Service + Target of each restored volume
}

// BackupItem records one mount backed up before a rollback and the object key of
// its archive within the backup session, so revert can stream it back.
type BackupItem struct {
	Service string
	Target  string
	Key     string
}

func createBackup(
	ctx context.Context, currentMounts []model.Mount, sess *backup.Session, eng volumeEngine, projectName string,
) ([]BackupItem, error) {
	log := zerolog.Ctx(ctx)

	items := make([]BackupItem, len(currentMounts))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for i, mt := range currentMounts {
		g.Go(func() error {
			w, key := sess.Writer(mt.Service, mt.Target)
			log.Info().Str("service", mt.Service).Str("mount", mt.Target).Msg("backing up mount")
			if _, err := volume.Copy(ctx, newEngineReader(eng, projectName, mt.Service, mt.Target), w); err != nil {
				return err
			}
			items[i] = BackupItem{Service: mt.Service, Target: mt.Target, Key: key}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return items, nil
}

// Restore the savepoint
func restoreBackup(
	ctx context.Context, record model.SavepointRecord, store *snapshot.Snapshot, eng volumeEngine, projectName string,
) error {
	log := zerolog.Ctx(ctx)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentVolumeOperations)
	for _, v := range record.Volumes {
		g.Go(func() error {
			log.Info().Str("service", v.Service).Str("mount", v.MountPath).Msg("restoring mount")
			r := store.Reader(v.Snapshot)
			w := newEngineWriter(eng, projectName, v.Service, v.MountPath)
			if _, err := volume.Copy(ctx, r, w); err != nil {
				return err
			}
			return nil
		})
	}
	return g.Wait()
}

// revert restores the pre-rollback state from the safety backup. It is used
// both when the savepoint restore fails and when the restored services come
// up unhealthy. Containers may be running (started but unhealthy), so they
// are stopped first — a no-op if the restore never got far enough to start
// them.
func revert(
	ctx context.Context,
	sess *backup.Session,
	items []BackupItem,
	dc composeRunner,
	eng volumeEngine,
	projectName string,
	waitTimeout int,
	cause error,
) error {
	log := zerolog.Ctx(ctx)
	backupRel := sess.RelPath()
	log.Warn().Err(cause).Str("backup", backupRel).Msg("rollback failed; reverting to the pre-rollback state")
	if err := dc.Stop(ctx); err != nil {
		return fmt.Errorf("rollback failed: %w; could not stop services to revert (backup retained at %s): %v", cause, backupRel, err)
	}
	var revertErr error
	for _, b := range items {
		r := sess.Reader(b.Key)
		w := newEngineWriter(eng, projectName, b.Service, b.Target)
		if _, err := volume.Copy(ctx, r, w); err != nil {
			revertErr = errors.Join(revertErr, err)
		}
	}
	log.Info().Msg("starting services")
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
// check on restart; progress is logged via the logger carried by ctx.
func (s *Service) Rollback(
	ctx context.Context, cfg config.Config, ref string, waitTimeout int,
) (RollbackResult, error) {
	log := zerolog.Ctx(ctx)

	records, err := s.mf.Load()
	if err != nil {
		return RollbackResult{}, fmt.Errorf("loading manifest.json file: %w", err)
	}
	record, err := findSavepoint(records, ref)
	if err != nil {
		return RollbackResult{}, fmt.Errorf("finding savepoint %q: %w", ref, err)
	}

	insp, err := s.compose.Inspect(ctx, cfg.ComposeFiles...)
	if err != nil {
		return RollbackResult{}, fmt.Errorf("inspecting compose files: %w", err)
	}
	currentMounts := insp.Mounts
	projectName := insp.ProjectName

	store := snapshot.New(s.subStorage(snapshotsDir(cfg)))

	// Pre-flight: every savepoint archive must exist before we touch anything.
	for _, v := range record.Volumes {
		exists, err := store.Exists(ctx, v.Snapshot)
		if err != nil {
			return RollbackResult{}, fmt.Errorf("checking savepoint archive for %s %s: %w", v.Service, v.MountPath, err)
		}
		if !exists {
			return RollbackResult{}, fmt.Errorf("savepoint archive missing for %s %s", v.Service, v.MountPath)
		}
	}

	dc := s.newCompose(cfg.ComposeFiles...)
	eng, err := s.newEngine()
	if err != nil {
		return RollbackResult{}, fmt.Errorf("creating docker engine: %w", err)
	}

	defer func() {
		if err := eng.Close(); err != nil {
			log.Warn().Err(err).Msg("failed to close docker engine")
		}
	}()

	log.Info().Msg("stopping services")
	if err := dc.Stop(ctx); err != nil {
		return RollbackResult{}, fmt.Errorf("stopping services: %w", err)
	}

	// Safety backup of the current live state, so a failed restore can be undone.
	sess := backup.NewSession(s.subStorage(filepath.Join(".gm", "tmp")), strconv.FormatInt(time.Now().Unix(), 10))
	items, err := createBackup(ctx, currentMounts, sess, eng, projectName)
	if err != nil {
		if rmErr := sess.Remove(ctx); rmErr != nil {
			log.Warn().Err(rmErr).Str("backup", sess.RelPath()).Msg("failed to remove incomplete backup")
		}
		log.Info().Msg("starting services")
		if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
			return RollbackResult{}, fmt.Errorf("pre-rollback backup failed: %w; additionally failed to restart services: %v", err, upErr)
		}
		return RollbackResult{}, fmt.Errorf("pre-rollback backup failed, nothing changed: %w", err)
	}

	// Restore the savepoint.
	err = restoreBackup(ctx, record, store, eng, projectName)
	if err != nil {
		return RollbackResult{}, revert(ctx, sess, items, dc, eng, projectName, waitTimeout, err)
	}

	// Bring services up and verify health. An unhealthy start (up -d --wait
	// fails) reverts to the pre-rollback state just like a restore failure.
	log.Info().Msg("starting services")
	if err := dc.Up(ctx, waitTimeout); err != nil {
		return RollbackResult{}, revert(ctx, sess, items, dc, eng, projectName, waitTimeout, fmt.Errorf("services did not become healthy after restore: %w", err))
	}

	// The rollback completed and services are healthy; drop the safety backup.
	if err := sess.Remove(ctx); err != nil {
		log.Warn().Err(err).Str("backup", sess.RelPath()).Msg("failed to remove safety backup")
	}

	restored := make([]model.Mount, 0, len(record.Volumes))
	for _, v := range record.Volumes {
		restored = append(restored, model.Mount{Service: v.Service, Target: v.MountPath})
	}
	return RollbackResult{Savepoint: record.Savepoint, Restored: restored}, nil
}
