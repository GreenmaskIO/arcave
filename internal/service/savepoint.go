package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	str2duration "github.com/xhit/go-str2duration/v2"

	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/client/docker"
	"github.com/greenmaskio/gm-tools/internal/model"
	"github.com/greenmaskio/gm-tools/internal/repository/manifest"
	"github.com/greenmaskio/gm-tools/internal/repository/snapshot"
	snapcopy "github.com/greenmaskio/gm-tools/internal/repository/snapshot/copy"
)

// Snapshot while the services are down; whatever happens here, the services
// must be started again before we return.
func (s *Service) snapshotMount(ctx context.Context, insp compose.Inspection, store *snapcopy.Store, sp model.Savepoint, record manifest.Record, mf *manifest.Manifest, eng *docker.Engine) error {
	log := zerolog.Ctx(ctx)
	for _, m := range insp.Mounts {
		dest, ref, err := store.Dest(sp.ID, m.Service, m.Target)
		if err != nil {
			return fmt.Errorf("getting snapshot destination: %v", err)
		}
		log.Info().Str("service", m.Service).Str("mount", m.Target).Msg("snapshotting mount")
		if err := eng.ExportMount(ctx, insp.ProjectName, m.Service, m.Target, dest); err != nil {
			return fmt.Errorf("exporting docker mounts/volumes: %v", err)
		}
		record.Volumes = append(record.Volumes, snapshot.VolumeRef{
			Service:   m.Service,
			MountPath: m.Target,
			Type:      m.Type,
			Source:    m.Source,
			Snapshot:  ref,
		})
	}
	mf.Savepoints = append(mf.Savepoints, record)
	return s.mf.Save(mf)
}

// CreateSavepoint stops the project services, snapshots every persistent mount
// into a new savepoint, records it in the manifest, and starts the services
// again. Progress is logged via the logger carried by ctx; the created
// savepoint metadata is returned.
func (s *Service) CreateSavepoint(ctx context.Context, name, description string) (_ model.Savepoint, err error) {
	log := zerolog.Ctx(ctx)

	cfg, err := s.cfg.Load()
	if err != nil {
		return model.Savepoint{}, err
	}

	insp, err := s.compose.Inspect(ctx, cfg.ComposeFiles...)
	if err != nil {
		return model.Savepoint{}, err
	}
	for _, sk := range insp.Skipped {
		log.Warn().Str("service", sk.Service).Str("reason", sk.Reason).Msg("mount will not be backed up")
	}
	if len(insp.Mounts) == 0 {
		return model.Savepoint{}, errors.New("no stateful mounts found; nothing to snapshot")
	}

	// Load the manifest before stopping services (so a corrupt index fails
	// fast) and so the new savepoint gets a non-colliding ID.
	mf, err := s.mf.Load()
	if err != nil {
		return model.Savepoint{}, err
	}

	now := time.Now()
	sp := model.Savepoint{
		ID:          uniqueSavepointID(now, mf.Savepoints),
		Name:        name,
		Description: description,
		CreatedAt:   now.UTC(),
	}
	record := manifest.Record{Savepoint: sp}

	store := snapcopy.New(s.projectRoot, snapshotsDir(cfg))

	dc := docker.NewComposeClient(cfg.ComposeFiles...)
	eng, err := docker.NewEngine()
	if err != nil {
		return model.Savepoint{}, err
	}
	defer func() {
		if cerr := eng.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close docker engine: %w", cerr)
		}
	}()

	log.Info().Msg("stopping services")
	if err := dc.Stop(ctx); err != nil {
		return model.Savepoint{}, err
	}

	// Snapshot while the services are down; whatever happens here, the services
	// must be started again before we return.
	snapErr := s.snapshotMount(ctx, insp, store, sp, record, mf, eng)
	if snapErr != nil {
		// Drop the partial snapshot so it can never be rolled back to.
		_ = store.Remove(sp.ID)
	}

	log.Info().Msg("starting services")
	upErr := dc.Up(ctx, docker.DefaultUpWaitTimeout)

	switch {
	case snapErr != nil && upErr != nil:
		return model.Savepoint{}, fmt.Errorf("%w; additionally, services failed to restart: %v", snapErr, upErr)
	case snapErr != nil:
		return model.Savepoint{}, snapErr
	case upErr != nil:
		return model.Savepoint{}, upErr
	}

	return sp, nil
}

// ListSavepoints returns every savepoint's metadata in manifest order.
func (s *Service) ListSavepoints() ([]model.Savepoint, error) {
	mf, err := s.mf.Load()
	if err != nil {
		return nil, fmt.Errorf("loading manifest.json file: %v", err)
	}
	return savepointsOf(mf.Savepoints), nil
}

// RemoveRequest selects savepoints to remove, either by explicit IDs or by
// retention rules (the two modes are mutually exclusive).
type RemoveRequest struct {
	IDs          []string
	RetainRecent int
	RetainFor    string
	BeforeDate   string
	DryRun       bool
}

// RemoveResult reports what was (or would be) removed.
type RemoveResult struct {
	DryRun  bool
	Removed []model.Savepoint
}

// RemoveSavepoints deletes savepoints selected by req. In retention mode, when
// several rules are combined a savepoint survives if any rule retains it.
func (s *Service) RemoveSavepoints(req RemoveRequest) (RemoveResult, error) {
	hasRetention := req.RetainRecent > 0 || req.RetainFor != "" || req.BeforeDate != ""

	if len(req.IDs) > 0 && hasRetention {
		return RemoveResult{}, errors.New("cannot combine savepoint IDs with --retain-recent/--retain-for/--before-date")
	}
	if len(req.IDs) == 0 && !hasRetention {
		return RemoveResult{}, errors.New("specify savepoint IDs, or at least one of --retain-recent, --retain-for or --before-date")
	}
	if (hasRetention && req.RetainRecent < 0) && req.RetainFor == "" && req.BeforeDate == "" {
		return RemoveResult{}, errors.New("--retain-recent must be zero or positive")
	}

	cfg, err := s.cfg.Load()
	if err != nil {
		return RemoveResult{}, fmt.Errorf("loading docker compose file: %w", err)
	}
	mf, err := s.mf.Load()
	if err != nil {
		return RemoveResult{}, fmt.Errorf("loading manifest.json file: %w", err)
	}

	var kept, removed []manifest.Record
	if len(req.IDs) > 0 {
		kept, removed, err = partitionByIDs(mf.Savepoints, req.IDs)
		if err != nil {
			return RemoveResult{}, err
		}
	} else {
		// Each cutoff marks savepoints created before it as deletable.
		var cutoffs []time.Time
		if req.RetainFor != "" {
			d, err := str2duration.ParseDuration(req.RetainFor)
			if err != nil {
				return RemoveResult{}, fmt.Errorf("invalid --retain-for %q: %w", req.RetainFor, err)
			}
			cutoffs = append(cutoffs, time.Now().UTC().Add(-d))
		}
		if req.BeforeDate != "" {
			t, err := time.Parse(time.RFC3339Nano, req.BeforeDate)
			if err != nil {
				return RemoveResult{}, fmt.Errorf("invalid --before-date %q: %w", req.BeforeDate, err)
			}
			cutoffs = append(cutoffs, t)
		}
		kept, removed = partitionSavepoints(mf.Savepoints, req.RetainRecent, cutoffs)
	}

	if !req.DryRun && len(removed) > 0 {
		store := snapcopy.New(s.projectRoot, snapshotsDir(cfg))
		for _, r := range removed {
			if err := store.Remove(r.ID); err != nil {
				return RemoveResult{}, fmt.Errorf("remove snapshot %s: %w", r.ID, err)
			}
		}
		mf.Savepoints = kept
		if err := s.mf.Save(mf); err != nil {
			return RemoveResult{}, fmt.Errorf("problem saving manifest.json: %w", err)
		}
	}

	return RemoveResult{DryRun: req.DryRun, Removed: savepointsOf(removed)}, nil
}

// uniqueSavepointID returns a Unix-second savepoint ID that does not collide
// with an existing savepoint. IDs double as the snapshot directory name, so two
// creates in the same second would otherwise share a directory and silently
// overwrite each other's data; the later one is bumped forward a second at a
// time until free.
func uniqueSavepointID(now time.Time, existing []manifest.Record) string {
	used := make(map[string]bool, len(existing))
	for _, r := range existing {
		used[r.ID] = true
	}
	for sec := now.Unix(); ; sec++ {
		id := strconv.FormatInt(sec, 10)
		if !used[id] {
			return id
		}
	}
}

// findSavepoint resolves a CLI ref to a manifest record. The ref may be
// "latest" (the newest savepoint), an exact savepoint ID, or a savepoint name.
// IDs take precedence since they are unique; a name that matches several
// savepoints is an error rather than a silent guess, since rolling back to the
// wrong one is destructive.
func findSavepoint(records []manifest.Record, ref string) (manifest.Record, error) {
	if len(records) == 0 {
		return manifest.Record{}, errors.New("no savepoints exist")
	}
	if ref == "latest" {
		latest := records[0]
		for _, r := range records[1:] {
			if r.CreatedAt.After(latest.CreatedAt) {
				latest = r
			}
		}
		return latest, nil
	}
	for _, r := range records {
		if r.ID == ref {
			return r, nil
		}
	}
	var byName []manifest.Record
	for _, r := range records {
		if r.Name == ref {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 0:
		return manifest.Record{}, fmt.Errorf("savepoint not found: %s", ref)
	case 1:
		return byName[0], nil
	default:
		ids := make([]string, len(byName))
		for i, r := range byName {
			ids[i] = r.ID
		}
		return manifest.Record{}, fmt.Errorf("%d savepoints are named %q; roll back by id instead: %s", len(byName), ref, strings.Join(ids, ", "))
	}
}

// partitionByIDs splits records into the ones whose ID was requested (removed)
// and the rest (kept). It errors if any requested ID is not present, so a typo
// never silently deletes nothing.
func partitionByIDs(records []manifest.Record, ids []string) (kept, removed []manifest.Record, err error) {
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
func partitionSavepoints(records []manifest.Record, retainRecent int, cutoffs []time.Time) (kept, removed []manifest.Record) {
	retainedByRecency := map[string]bool{}
	hasRecent := retainRecent > 0
	if hasRecent {
		byNewest := append([]manifest.Record(nil), records...)
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
