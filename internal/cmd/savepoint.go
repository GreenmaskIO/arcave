package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	str2duration "github.com/xhit/go-str2duration/v2"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/client/docker"
)

// savepointMeta is the JSON shape printed for a savepoint by create, list and
// remove (the manifest record minus internal volume bookkeeping).
type savepointMeta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func metaOf(r savepointRecord) savepointMeta {
	return savepointMeta{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
	}
}

func newSavepointCmd() *cobra.Command {
	sp := &cobra.Command{
		Use:   "savepoint",
		Short: "Manage environment savepoints",
	}
	sp.AddCommand(
		newSavepointCreateCmd(),
		newSavepointListCmd(),
		newSavepointRemoveCmd(),
	)
	return sp
}

// snapshotArchiveName is the tar archive holding one volume's data, stored
// under <savepoint-id>/<service-name>/<volume-mounted-path>/.
const snapshotArchiveName = "data.tar"

// mountDirName turns a container mount path into a single folder name by
// replacing each "/" with "_", so the whole mount path becomes one directory
// segment, e.g. "/export/adventureworks1" -> "_export_adventureworks1".
func mountDirName(target string) string {
	return strings.ReplaceAll(target, "/", "_")
}

// snapshotsRoot returns the directory holding all savepoint data, relative to
// the project root unless the config overrides it with an absolute path.
func snapshotsRoot(cfg *projectConfig) string {
	if cfg.SavepointDir != "" {
		return cfg.SavepointDir
	}
	return filepath.Join(gmDir, "snapshots")
}

func newSavepointCreateCmd() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new environment savepoint",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if name != "" && name != args[0] {
					return errors.New("savepoint name given both as argument and --name")
				}
				name = args[0]
			}
			return runSavepointCreate(cmd, name, description)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Savepoint name")
	cmd.Flags().StringVar(&description, "description", "", "Savepoint description")
	return cmd
}

// runSavepointCreate stops the project services, copies every named volume of
// the stateful services into a new snapshot directory, records the savepoint
// in the manifest, and starts the services again.
func runSavepointCreate(cmd *cobra.Command, name, description string) error {
	ctx := cmd.Context()
	errOut := cmd.ErrOrStderr()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	project, projectName, err := compose.Load(ctx, cfg.ComposeFiles...)
	if err != nil {
		return err
	}

	mounts, skipped := compose.Mounts(project)
	for _, s := range skipped {
		fmt.Fprintf(errOut, "warning: %s: %s will not be backed up\n", s.Service, s.Reason)
	}
	if len(mounts) == 0 {
		return errors.New("no stateful mounts found; nothing to snapshot")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}

	now := time.Now()
	record := savepointRecord{
		ID:          strconv.FormatInt(now.Unix(), 10),
		Name:        name,
		Description: description,
		CreatedAt:   now.UTC(),
	}
	snapDir := filepath.Join(snapshotsRoot(cfg), record.ID)
	absSnapDir := snapDir
	if !filepath.IsAbs(absSnapDir) {
		absSnapDir = filepath.Join(cwd, snapDir)
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

	// Snapshot while the services are down; whatever happens here, the
	// services must be started again before we return.
	snapErr := func() error {
		for _, m := range mounts {
			// .gm/snapshots/<id>/<service-name>/<volume-mounted-path>/data.tar
			dir := mountDirName(m.Target)
			relDir := filepath.Join(snapDir, m.Service, dir)
			absDir := filepath.Join(absSnapDir, m.Service, dir)
			if err := os.MkdirAll(absDir, 0o755); err != nil {
				return fmt.Errorf("create snapshot directory: %w", err)
			}
			rel := filepath.Join(relDir, snapshotArchiveName)
			abs := filepath.Join(absDir, snapshotArchiveName)
			fmt.Fprintf(errOut, "Snapshotting %s:%s...\n", m.Service, m.Target)
			if err := eng.ExportMount(ctx, projectName, m.Service, m.Target, abs); err != nil {
				return err
			}
			record.Volumes = append(record.Volumes, savepointVolume{
				Service:   m.Service,
				MountPath: m.Target,
				Type:      m.Type,
				Source:    m.Source,
				Path:      rel,
			})
		}

		m, err := loadManifest()
		if err != nil {
			return err
		}
		m.Savepoints = append(m.Savepoints, record)
		return saveManifest(m)
	}()
	if snapErr != nil {
		// Drop the partial snapshot so it can never be rolled back to.
		os.RemoveAll(absSnapDir)
	}

	fmt.Fprintln(errOut, "Starting services...")
	upErr := dc.Up(ctx)

	if snapErr != nil {
		return snapErr
	}
	if upErr != nil {
		return upErr
	}

	return writeJSON(cmd.OutOrStdout(), metaOf(record))
}

func newSavepointListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available savepoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := loadManifest()
			if err != nil {
				return err
			}
			metas := make([]savepointMeta, 0, len(m.Savepoints))
			for _, r := range m.Savepoints {
				metas = append(metas, metaOf(r))
			}
			return writeJSON(cmd.OutOrStdout(), metas)
		},
	}
}

func newSavepointRemoveCmd() *cobra.Command {
	var (
		retainRecent int
		retainFor    string
		beforeDate   string
		dryRun       bool
	)

	cmd := &cobra.Command{
		Use:   "remove [id...]",
		Short: "Remove savepoints by ID, or by retention rules",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSavepointRemove(cmd, args, retainRecent, retainFor, beforeDate, dryRun)
		},
	}
	cmd.Flags().IntVar(&retainRecent, "retain-recent", 0, "Retain the most recent N savepoints")
	cmd.Flags().StringVar(&retainFor, "retain-for", "", "Retain savepoints for the specified duration (e.g. 1w2d3h4m5s)")
	cmd.Flags().StringVar(&beforeDate, "before-date", "", "Delete savepoints older than RFC3339Nano timestamp")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show savepoints that would be deleted without deleting them")
	return cmd
}

// runSavepointRemove deletes savepoints selected either by explicit IDs (the
// positional args) or by the retention flags — the two modes are mutually
// exclusive. In retention mode, when several flags are combined a savepoint
// survives if any rule retains it.
func runSavepointRemove(cmd *cobra.Command, ids []string, retainRecent int, retainFor, beforeDate string, dryRun bool) error {
	hasRecent := cmd.Flags().Changed("retain-recent")
	hasRetention := hasRecent || retainFor != "" || beforeDate != ""

	if len(ids) > 0 && hasRetention {
		return errors.New("cannot combine savepoint IDs with --retain-recent/--retain-for/--before-date")
	}
	if len(ids) == 0 && !hasRetention {
		return errors.New("specify savepoint IDs, or at least one of --retain-recent, --retain-for or --before-date")
	}
	if hasRecent && retainRecent < 0 {
		return errors.New("--retain-recent must be zero or positive")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	m, err := loadManifest()
	if err != nil {
		return err
	}

	var kept, removed []savepointRecord
	if len(ids) > 0 {
		kept, removed, err = partitionByIDs(m.Savepoints, ids)
		if err != nil {
			return err
		}
	} else {
		// Each cutoff marks savepoints created before it as deletable.
		var cutoffs []time.Time
		if retainFor != "" {
			d, err := str2duration.ParseDuration(retainFor)
			if err != nil {
				return fmt.Errorf("invalid --retain-for %q: %w", retainFor, err)
			}
			cutoffs = append(cutoffs, time.Now().UTC().Add(-d))
		}
		if beforeDate != "" {
			t, err := time.Parse(time.RFC3339Nano, beforeDate)
			if err != nil {
				return fmt.Errorf("invalid --before-date %q: %w", beforeDate, err)
			}
			cutoffs = append(cutoffs, t)
		}
		kept, removed = partitionSavepoints(m.Savepoints, hasRecent, retainRecent, cutoffs)
	}

	if !dryRun && len(removed) > 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		for _, r := range removed {
			dir := filepath.Join(snapshotsRoot(cfg), r.ID)
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, dir)
			}
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove snapshot %s: %w", r.ID, err)
			}
		}
		m.Savepoints = kept
		if err := saveManifest(m); err != nil {
			return err
		}
	}

	metas := make([]savepointMeta, 0, len(removed))
	for _, r := range removed {
		metas = append(metas, metaOf(r))
	}
	return writeJSON(cmd.OutOrStdout(), struct {
		DryRun  bool            `json:"dry_run"`
		Removed []savepointMeta `json:"removed"`
	}{dryRun, metas})
}

// partitionByIDs splits records into the ones whose ID was requested (removed)
// and the rest (kept). It errors if any requested ID is not present, so a typo
// never silently deletes nothing.
func partitionByIDs(records []savepointRecord, ids []string) (kept, removed []savepointRecord, err error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	seen := make(map[string]bool, len(ids))
	for _, r := range records {
		if want[r.ID] {
			removed = append(removed, r)
			seen[r.ID] = true
		} else {
			kept = append(kept, r)
		}
	}
	var missing []string
	for _, id := range ids {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("savepoint(s) not found: %s", strings.Join(missing, ", "))
	}
	return kept, removed, nil
}

// partitionSavepoints splits records into the ones to keep and the ones to
// remove. A record is removed only when every active rule marks it deletable:
// it is outside the retainRecent newest (when hasRecent) and older than every
// cutoff.
func partitionSavepoints(records []savepointRecord, hasRecent bool, retainRecent int, cutoffs []time.Time) (kept, removed []savepointRecord) {
	retainedByRecency := map[string]bool{}
	if hasRecent {
		byNewest := append([]savepointRecord(nil), records...)
		sort.Slice(byNewest, func(i, j int) bool {
			return byNewest[i].CreatedAt.After(byNewest[j].CreatedAt)
		})
		for i := 0; i < retainRecent && i < len(byNewest); i++ {
			retainedByRecency[byNewest[i].ID] = true
		}
	}

	for _, r := range records {
		del := !hasRecent || !retainedByRecency[r.ID]
		for _, c := range cutoffs {
			if !r.CreatedAt.Before(c) {
				del = false
			}
		}
		if del {
			removed = append(removed, r)
		} else {
			kept = append(kept, r)
		}
	}
	return kept, removed
}
