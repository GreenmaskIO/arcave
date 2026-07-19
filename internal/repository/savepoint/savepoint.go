// Package savepoint reads and writes savepoint records — one JSON object per
// savepoint, stored at the root of that savepoint's own namespace.
//
// The record is the commit marker. It is written last on create and deleted
// first on remove, so the only states a reader can observe are "fully present"
// and "gone". A savepoint whose data was written but whose record never was is
// incomplete: List skips it, and it is safe to garbage-collect.
//
// This replaces a single manifest.json holding every savepoint. Storager writes
// are not atomic on purpose (see the directory backend), so a torn write to a
// monolithic index destroyed the whole savepoint index rather than the one
// savepoint being written. Per-savepoint records shrink that blast radius to the
// savepoint that was mid-write, and they delete the create-time race in which a
// second process's manifest was clobbered by the first's stale in-memory copy.
//
// The store is the savepoint store — the same model.Storager the snapshot
// backend writes archives into — so records always travel with the data they
// describe. A savepoint store on S3 or a shared mount carries its own records;
// nothing here assumes a local directory.
package savepoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
)

// recordName is the reserved key at the root of every savepoint namespace. The
// namespace directory belongs to this layer; a backend may write anything else
// beneath it, and must never mint an Opaque that collides with this key.
const recordName = "savepoint.json"

// legacyManifestName is the monolithic index this package replaced. Its presence
// at the store root means the store predates per-savepoint records.
const legacyManifestName = "manifest.json"

// schemaVersion is the record's own version, independent of the config's. It is
// written into every record so a future reader can tell what it is holding.
const schemaVersion = 1

// file is the on-disk shape of savepoint.json: the shared record, flattened,
// plus its schema version. The envelope is private to the repository, so the
// Save/List contract is expressed purely in the shared model types.
type file struct {
	SchemaVersion int `json:"schema_version"`
	model.SavepointRecord
}

// Repository reads and writes savepoint records inside store.
type Repository struct {
	// storage is re-rooted at the savepoint store, shared with the backend that
	// writes the archives these records describe.
	storage model.Storager
}

// New returns a savepoint repository over store, which must already be re-rooted
// at the store whose savepoints it records.
func New(store model.Storager) *Repository {
	return &Repository{storage: store}
}

// Save writes "<id>/savepoint.json". It is the commit: the caller must have
// written every archive first, because a reader treats this object's existence
// as proof that the savepoint is whole.
func (r *Repository) Save(ctx context.Context, record model.SavepointRecord) error {
	if err := model.ValidSavepointID(record.ID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file{SchemaVersion: schemaVersion, SavepointRecord: record}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal savepoint %s: %w", record.ID, err)
	}
	data = append(data, '\n')

	if err := r.storage.PutObject(ctx, recordKey(record.ID), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write savepoint record %s: %w", record.ID, err)
	}
	return nil
}

// List returns every committed record.
//
// It walks the store's namespace directories, keeps those whose name is a valid
// savepoint id, and reads each one's record. A namespace with no record is an
// incomplete savepoint — a create that died before committing — and is skipped;
// a record that is present but will not decode is a corrupt commit and is an
// error, never a silent skip.
//
// That is N+1 round-trips rather than a single index read, which is free on a
// local filesystem and, on an object store, collapses into one prefix listing.
func (r *Repository) List(ctx context.Context) ([]model.SavepointRecord, error) {
	if err := r.rejectLegacyManifest(ctx); err != nil {
		return nil, err
	}

	_, dirs, err := r.storage.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list savepoint store: %w", err)
	}

	records := make([]model.SavepointRecord, 0, len(dirs))
	for _, dir := range dirs {
		id := dir.Dirname()
		if err := model.ValidSavepointID(id); err != nil {
			log.Ctx(ctx).Debug().Str("entry", id).Msg("ignoring directory that is not a savepoint namespace")
			continue
		}
		record, found, err := r.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			log.Ctx(ctx).Debug().Str("savepoint", id).Msg("skipping incomplete savepoint: no record")
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

// rejectLegacyManifest refuses a store still holding the monolithic index.
//
// Silence is the dangerous outcome here. Every namespace of a legacy store holds
// archives but no savepoint.json, so List would classify each as incomplete and
// skip it — reporting "no savepoints" to a user whose data is sitting on disk.
// That reads as data loss, so the store is rejected before anything is skipped.
func (r *Repository) rejectLegacyManifest(ctx context.Context) error {
	exists, err := r.storage.Exists(ctx, legacyManifestName)
	if err != nil {
		return fmt.Errorf("stat %s: %w", legacyManifestName, err)
	}
	if exists {
		return fmt.Errorf("%w: %s", model.ErrLegacyManifest, path.Join(r.storage.GetCwd(), legacyManifestName))
	}
	return nil
}

// load reads one savepoint's record. found is false when the namespace holds no
// record, which means the savepoint never committed.
func (r *Repository) load(ctx context.Context, id string) (model.SavepointRecord, bool, error) {
	// Exists rather than matching GetObject's error against os.ErrNotExist: that
	// error belongs to the directory backend, and an object store would report a
	// missing key differently.
	exists, err := r.storage.Exists(ctx, recordKey(id))
	if err != nil {
		return model.SavepointRecord{}, false, fmt.Errorf("stat savepoint record %s: %w", id, err)
	}
	if !exists {
		return model.SavepointRecord{}, false, nil
	}

	src, err := r.storage.GetObject(ctx, recordKey(id))
	if err != nil {
		return model.SavepointRecord{}, false, fmt.Errorf("read savepoint record %s: %w", id, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("savepoint", id).Msg("close savepoint record")
		}
	}()

	var f file
	if err := json.NewDecoder(src).Decode(&f); err != nil {
		return model.SavepointRecord{}, false, fmt.Errorf("parse savepoint record %s: %w", id, err)
	}
	return f.SavepointRecord, true, nil
}

// Delete removes one savepoint's record. It is the uncommit: the caller must run
// it *before* dropping the savepoint's data, so a crash between the two leaves an
// orphaned namespace (invisible, collectable) rather than a savepoint that still
// lists and still resolves but whose archives are half gone.
//
// It is idempotent: a namespace with no record is already uncommitted.
func (r *Repository) Delete(ctx context.Context, id string) error {
	if err := model.ValidSavepointID(id); err != nil {
		return err
	}
	exists, err := r.storage.Exists(ctx, recordKey(id))
	if err != nil {
		return fmt.Errorf("stat savepoint record %s: %w", id, err)
	}
	if !exists {
		return nil
	}
	if err := r.storage.Delete(ctx, recordKey(id)); err != nil {
		return fmt.Errorf("delete savepoint record %s: %w", id, err)
	}
	return nil
}

// recordKey is the record's key relative to the store root. Every caller has
// already validated id, so this never mints a key that escapes the store.
func recordKey(id string) string {
	return path.Join(id, recordName)
}
