// Package savepoint reads and writes savepoint records — one JSON object per
// savepoint, stored at the root of that savepoint's own namespace.
//
// The record is the commit marker. It is written last on create and deleted
// first on remove, so the only states a reader can observe are "fully present"
// and "gone". A savepoint whose data was written but whose record never was is
// incomplete: ListSavepoints skips it, and it is safe to garbage-collect.
//
// This replaces a single manifest.json holding every savepoint. Storager writes
// are not atomic on purpose (see the directory backend), so a torn write to a
// monolithic index destroyed the whole savepoint index rather than the one
// savepoint being written. Per-savepoint records shrink that blast radius to the
// savepoint that was mid-write, and they delete the create-time race in which a
// second process's manifest was clobbered by the first's stale in-memory copy.
//
// The store is the savepoint store — the same storages.Storager the snapshot
// backend writes archives into — so records always travel with the data they
// describe. A savepoint store on S3 or a shared mount carries its own records;
// nothing here assumes a local directory.
package savepoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
)

// The reserved keys at the root of every savepoint namespace. The namespace
// directory belongs to this layer; a backend may write anything else beneath it,
// and must never create an Opaque that collides with either key.
const (
	recordName = "savepoint.json"
	specName   = "project.yaml" // the compose spec the savepoint was taken under
)

// Repository reads and writes savepoint records inside store.
type Repository struct {
	// storage is re-rooted at the savepoint store, shared with the backend that
	// writes the archives these records describe.
	storage storages.Storager
}

// New returns a savepoint repository over store, which must already be re-rooted
// at the store whose savepoints it records.
func New(store storages.Storager) *Repository {
	return &Repository{storage: store}
}

// SaveSavepoint writes "<id>/savepoint.json". It is the commit: the caller must have
// written every archive first, because a reader treats this object's existence
// as proof that the savepoint is whole.
func (r *Repository) SaveSavepoint(ctx context.Context, savepoint model.Savepoint) error {
	if err := savepoint.ID.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(savepoint, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal savepoint %s: %w", savepoint.ID, err)
	}
	data = append(data, '\n')

	if err := r.storage.PutObject(ctx, recordKey(savepoint.ID), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write savepoint record %s: %w", savepoint.ID, err)
	}
	return nil
}

// SaveSpec writes "<id>/project.yaml". It must run before SaveSavepoint: the record
// names the artifact, so a record committed without it would name nothing.
func (r *Repository) SaveSpec(ctx context.Context, id model.SavepointID, spec []byte) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := r.storage.PutObject(ctx, specKey(id), bytes.NewReader(spec)); err != nil {
		return fmt.Errorf("write savepoint spec %s: %w", id, err)
	}
	return nil
}

// LoadSpec reads one savepoint's compose spec. A missing artifact is an error,
// not an empty result: a record naming one is a statement that it is there.
func (r *Repository) LoadSpec(ctx context.Context, id model.SavepointID) ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}

	src, err := r.storage.GetObject(ctx, specKey(id))
	if err != nil {
		return nil, fmt.Errorf("read savepoint spec %s: %w", id, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("savepoint", id.String()).Msg("close savepoint spec")
		}
	}()

	spec, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read savepoint spec %s: %w", id, err)
	}
	return spec, nil
}

// ListSavepoints returns every committed record.
//
// It walks the store's namespace directories, keeps those whose name is a valid
// savepoint id, and reads each one's record. A namespace with no record is an
// incomplete savepoint — a create that died before committing — and is skipped;
// a record that is present but will not decode is a corrupt commit and is an
// error, never a silent skip.
//
// That is N+1 round-trips rather than a single index read, which is free on a
// local filesystem and, on an object store, collapses into one prefix listing.
func (r *Repository) ListSavepoints(ctx context.Context) ([]model.Savepoint, error) {
	_, dirs, err := r.storage.ListDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("list savepoint store: %w", err)
	}

	records := make([]model.Savepoint, 0, len(dirs))
	for _, dir := range dirs {
		id := model.SavepointID(dir.Dirname())
		if err := id.Validate(); err != nil {
			log.Ctx(ctx).Debug().Str("entry", id.String()).Msg("ignoring directory that is not a savepoint namespace")
			continue
		}
		record, found, err := r.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			log.Ctx(ctx).Debug().Str("savepoint", id.String()).Msg("skipping incomplete savepoint: no record")
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

// load reads one savepoint's record. found is false when the namespace holds no
// record, which means the savepoint never committed.
func (r *Repository) load(ctx context.Context, id model.SavepointID) (model.Savepoint, bool, error) {
	// Exists rather than matching GetObject's error against os.ErrNotExist: that
	// error belongs to the directory backend, and an object store would report a
	// missing key differently.
	exists, err := r.storage.Exists(ctx, recordKey(id))
	if err != nil {
		return model.Savepoint{}, false, fmt.Errorf("stat savepoint record %s: %w", id, err)
	}
	if !exists {
		return model.Savepoint{}, false, nil
	}

	src, err := r.storage.GetObject(ctx, recordKey(id))
	if err != nil {
		return model.Savepoint{}, false, fmt.Errorf("read savepoint record %s: %w", id, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("savepoint", id.String()).Msg("close savepoint record")
		}
	}()

	var record model.Savepoint
	if err := json.NewDecoder(src).Decode(&record); err != nil {
		return model.Savepoint{}, false, fmt.Errorf("parse savepoint record %s: %w", id, err)
	}
	return record, true, nil
}

// DeleteSavepoint removes one savepoint's record. It is the uncommit: the caller must run
// it *before* dropping the savepoint's data, so a crash between the two leaves an
// orphaned namespace (invisible, collectable) rather than a savepoint that still
// lists and still resolves but whose archives are half gone.
//
// It is idempotent: a namespace with no record is already uncommitted.
func (r *Repository) DeleteSavepoint(ctx context.Context, id model.SavepointID) error {
	if err := id.Validate(); err != nil {
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
// already validated id, so this never builds a key that escapes the store.
func recordKey(id model.SavepointID) string {
	return path.Join(id.String(), recordName)
}

// specKey is the spec artifact's key, beside the record it belongs to.
func specKey(id model.SavepointID) string {
	return path.Join(id.String(), specName)
}
