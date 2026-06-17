package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/client/docker"
)

// restoredMount is the per-mount entry in the rollback JSON output.
type restoredMount struct {
	Service   string `json:"service"`
	MountPath string `json:"mount_path"`
}

func newRollbackCmd() *cobra.Command {
	waitTimeout := docker.DefaultUpWaitTimeout

	cmd := &cobra.Command{
		Use:   "rollback [savepoint-id|name|latest]",
		Short: "Restore the environment to a specified savepoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd, args[0], waitTimeout)
		},
	}
	cmd.Flags().IntVar(&waitTimeout, "wait-timeout", docker.DefaultUpWaitTimeout,
		"Seconds to wait for services to become healthy before reverting")
	return cmd
}

// findSavepoint resolves a CLI ref to a manifest record. The ref may be
// "latest" (the newest savepoint), an exact savepoint ID, or a savepoint name.
// IDs take precedence since they are unique; a name that matches several
// savepoints is an error rather than a silent guess, since rolling back to the
// wrong one is destructive.
func findSavepoint(m *manifest, ref string) (savepointRecord, error) {
	if len(m.Savepoints) == 0 {
		return savepointRecord{}, errors.New("no savepoints exist")
	}
	if ref == "latest" {
		latest := m.Savepoints[0]
		for _, r := range m.Savepoints[1:] {
			if r.CreatedAt.After(latest.CreatedAt) {
				latest = r
			}
		}
		return latest, nil
	}
	for _, r := range m.Savepoints {
		if r.ID == ref {
			return r, nil
		}
	}
	var byName []savepointRecord
	for _, r := range m.Savepoints {
		if r.Name == ref {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 0:
		return savepointRecord{}, fmt.Errorf("savepoint not found: %s", ref)
	case 1:
		return byName[0], nil
	default:
		ids := make([]string, len(byName))
		for i, r := range byName {
			ids[i] = r.ID
		}
		return savepointRecord{}, fmt.Errorf("%d savepoints are named %q; roll back by id instead: %s", len(byName), ref, strings.Join(ids, ", "))
	}
}

// runRollback restores the environment to the referenced savepoint. It stops the
// services, backs up their current state under .gm/tmp so a failed restore can be
// undone, wipes and re-extracts each savepoint volume, then restarts the
// services. If any restore step fails, the pre-rollback backup is replayed to
// return the environment to where it started.
func runRollback(cmd *cobra.Command, ref string, waitTimeout int) error {
	ctx := cmd.Context()
	errOut := cmd.ErrOrStderr()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	m, err := loadManifest()
	if err != nil {
		return err
	}
	record, err := findSavepoint(m, ref)
	if err != nil {
		return err
	}

	project, projectName, err := compose.Load(ctx, cfg.ComposeFiles...)
	if err != nil {
		return err
	}
	currentMounts, _ := compose.Mounts(project)

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(cwd, p)
	}

	// Pre-flight: every savepoint archive must exist before we touch anything.
	for _, v := range record.Volumes {
		if _, err := os.Stat(abs(v.Path)); err != nil {
			return fmt.Errorf("savepoint archive missing for %s %s: %w", v.Service, v.MountPath, err)
		}
	}

	dc := docker.NewComposeClient(cfg.ComposeFiles...)
	eng, err := docker.NewEngine()
	if err != nil {
		return err
	}
	defer eng.Close()

	fmt.Fprintln(errOut, "Stopping services...")
	if err := dc.Stop(ctx); err != nil {
		return err
	}

	// Safety backup of the current live state, so a failed restore can be undone.
	backupRel := filepath.Join(gmDir, "tmp", "rollback-"+strconv.FormatInt(time.Now().Unix(), 10))
	backupAbs := abs(backupRel)
	type backupItem struct{ service, target, tar string }
	var backup []backupItem
	backupErr := func() error {
		for _, mt := range currentMounts {
			tar := filepath.Join(backupAbs, mt.Service, mountDirName(mt.Target), snapshotArchiveName)
			if err := os.MkdirAll(filepath.Dir(tar), 0o755); err != nil {
				return fmt.Errorf("create backup directory: %w", err)
			}
			fmt.Fprintf(errOut, "Backing up %s:%s...\n", mt.Service, mt.Target)
			if err := eng.ExportMount(ctx, projectName, mt.Service, mt.Target, tar); err != nil {
				return err
			}
			backup = append(backup, backupItem{mt.Service, mt.Target, tar})
		}
		return nil
	}()
	if backupErr != nil {
		os.RemoveAll(backupAbs)
		fmt.Fprintln(errOut, "Starting services...")
		if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
			return fmt.Errorf("pre-rollback backup failed: %w; additionally failed to restart services: %v", backupErr, upErr)
		}
		return fmt.Errorf("pre-rollback backup failed, nothing changed: %w", backupErr)
	}

	// revert restores the pre-rollback state from the safety backup. It is used
	// both when the savepoint restore fails and when the restored services come
	// up unhealthy. Containers may be running (started but unhealthy), so they
	// are stopped first — a no-op if the restore never got far enough to start
	// them.
	revert := func(cause error) error {
		fmt.Fprintf(errOut, "Rollback failed: %v\nReverting to the pre-rollback state from %s...\n", cause, backupRel)
		if err := dc.Stop(ctx); err != nil {
			return fmt.Errorf("rollback failed: %w; could not stop services to revert (backup retained at %s): %v", cause, backupRel, err)
		}
		var revertErr error
		for _, b := range backup {
			if err := eng.RestoreMount(ctx, projectName, b.service, b.target, b.tar); err != nil {
				revertErr = errors.Join(revertErr, err)
			}
		}
		fmt.Fprintln(errOut, "Starting services...")
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
			fmt.Fprintf(errOut, "Restoring %s:%s...\n", v.Service, v.MountPath)
			if err := eng.RestoreMount(ctx, projectName, v.Service, v.MountPath, abs(v.Path)); err != nil {
				return err
			}
		}
		return nil
	}()
	if restoreErr != nil {
		return revert(restoreErr)
	}

	// Bring services up and verify health. An unhealthy start (up -d --wait
	// fails) reverts to the pre-rollback state just like a restore failure.
	fmt.Fprintln(errOut, "Starting services...")
	if upErr := dc.Up(ctx, waitTimeout); upErr != nil {
		return revert(fmt.Errorf("services did not become healthy after restore: %w", upErr))
	}

	// The rollback completed and services are healthy; drop the safety backup.
	os.RemoveAll(backupAbs)
	os.Remove(abs(filepath.Join(gmDir, "tmp"))) // drop the parent if now empty

	restored := make([]restoredMount, 0, len(record.Volumes))
	for _, v := range record.Volumes {
		restored = append(restored, restoredMount{Service: v.Service, MountPath: v.MountPath})
	}
	return writeJSON(cmd.OutOrStdout(), struct {
		RolledBackTo savepointMeta   `json:"rolled_back_to"`
		Restored     []restoredMount `json:"restored"`
	}{metaOf(record), restored})
}
