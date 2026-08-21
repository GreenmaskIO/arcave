// Package repo holds the objects of one restic repository: which key each lands
// under, and which store failure is one of the two conditions the protocol has a
// status for. Nothing here names a status, and nothing above it names a key.
package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// configKey is the repository config, the one object that lives at the store
// root rather than under a type. It is the storage key, not the URL path the
// route table matches: the two spell the same word and mean different things.
const configKey = "config"

var _ model.Repository = (*Repository)(nil)

// Repository is one restic repository held in one Storager, and the whole of
// what this package does with storage: it renders the keys, decides which of
// them is a blob of this repository, and reduces a store failure to the two
// conditions the protocol has a status for.
//
// It holds no state beyond the store, so it is safe to serve concurrently:
// per-key atomicity is the Storager's job.
type Repository struct {
	store storages.Storager
}

// New returns the repository over store, which must already be rooted at it.
func New(store storages.Storager) *Repository {
	return &Repository{store: store}
}

// Create answers `restic init`.
//
// It is a no-op that reports success. rest-server creates the repository
// directory, the five type directories and data/00..ff; a Storager has no
// directories to create — the backend materialises whatever parents a key needs
// when the object is written — so there is nothing to do and nothing that could
// fail. It stays on the seam because a repository that does have something to
// create is the only thing that would need it back.
func (s *Repository) Create(ctx context.Context) error {
	zerolog.Ctx(ctx).Debug().Str("store", s.store.GetCwd()).Msg("restic repository created")
	return nil
}

// StatConfig reports the config's size, or ErrObjectNotFound. restic asks it to
// decide whether a repository is initialised at all.
func (s *Repository) StatConfig(_ context.Context) (int64, error) {
	return s.statObject(configKey)
}

// OpenConfig opens the config for reading, whole: it is small, never fetched by
// range, and the caller streams it out unchanged.
func (s *Repository) OpenConfig(ctx context.Context) (io.ReadCloser, error) {
	rc, err := s.store.GetObject(ctx, configKey)
	if err != nil {
		if errors.Is(err, storages.ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s", model.ErrObjectNotFound, configKey)
		}
		return nil, fmt.Errorf("open %s: %w", configKey, err)
	}
	return rc, nil
}

// SaveConfig writes the config at init time, and refuses to replace one that is
// already there with ErrConfigExists.
//
// The refusal is the whole point: rest-server opens the file O_EXCL so that a
// restic init can never silently re-initialise a repository that already holds
// data, and restic reports exactly that to the user. The Exists/PutObject pair
// is not atomic and does not need to be — init is a single client doing one
// thing.
func (s *Repository) SaveConfig(ctx context.Context, body io.Reader) error {
	exists, err := s.store.Exists(ctx, configKey)
	if err != nil {
		return fmt.Errorf("checking %s: %w", configKey, err)
	}
	if exists {
		return model.ErrConfigExists
	}
	if err := s.store.PutObject(ctx, configKey, body); err != nil {
		return fmt.Errorf("writing %s: %w", configKey, err)
	}
	return nil
}

// DeleteConfig removes the config, idempotently.
func (s *Repository) DeleteConfig(ctx context.Context) error {
	return s.deleteObject(ctx, configKey)
}

// ListBlobs reports every blob of one type, from a single List of that type's
// prefix.
//
// A type holding nothing is an empty slice, never an error: Create makes no
// directories here, so "absent" and "empty" are the same state, and restic must
// be able to list a fresh repository.
func (s *Repository) ListBlobs(ctx context.Context, req model.ListBlobsRequest) ([]model.Blob, error) {
	objectType := string(req.Type)
	stats, err := s.store.List(ctx, objectType)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", objectType, err)
	}

	blobs := make([]model.Blob, 0, len(stats))
	for _, st := range stats {
		if !isBlobName(st.Name) {
			continue
		}
		blobs = append(blobs, model.Blob{Name: st.Name, Size: st.Size})
	}

	// WARN, and counted rather than named one by one: a key under a type that
	// this repository did not write means the store root is shared with
	// something else — two repositories bound at one prefix, or a store rooted
	// one level too high — and the listing restic gets is silently short.
	if skipped := len(stats) - len(blobs); skipped > 0 {
		zerolog.Ctx(ctx).Warn().Str("type", objectType).Int("skipped", skipped).
			Msg("keys under this type are not blobs of this repository and were left out of the listing")
	}
	return blobs, nil
}

// StatBlob reports one blob's size, or ErrObjectNotFound. restic asks it to
// decide whether it already holds a pack.
func (s *Repository) StatBlob(_ context.Context, req model.StatBlobRequest) (int64, error) {
	return s.statObject(blobKey(req.Type, req.ID))
}

// OpenBlobRange opens the interval the request names, and nothing outside it:
// the store transfers the requested bytes rather than the object, which is what
// lets restic read one blob out of a large pack.
//
// A zero-length interval is answered without asking the store: an empty blob
// legitimately has one, and every range over a zero-length object is
// ErrInvalidRange down there.
func (s *Repository) OpenBlobRange(ctx context.Context, req model.OpenBlobRangeRequest) (io.ReadCloser, error) {
	if req.Length == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}

	key := blobKey(req.Type, req.ID)
	rc, err := s.store.GetObjectRange(ctx, key, req.Offset, req.Length)
	if err != nil {
		if errors.Is(err, storages.ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s", model.ErrObjectNotFound, key)
		}
		return nil, fmt.Errorf("open %s at %d+%d: %w", key, req.Offset, req.Length, err)
	}
	return rc, nil
}

// SaveBlob stores an uploaded pack, index, snapshot, key or lock.
//
// The body is streamed straight into the store: no temp object, no rename, no
// fsync, and no check that the content hashes to the name it was uploaded under
// (which rest-server itself makes optional). Blob names are content-addressed,
// so an upload of a name that already exists carries the same bytes — arcave
// takes the overwrite rather than rest-server's refusal, because a retried
// upload after a half-written object is the case that matters, and it must not
// be refused.
//
// (Append-only mode, when it lands, refuses an overwrite here.)
func (s *Repository) SaveBlob(ctx context.Context, req model.SaveBlobRequest) error {
	key := blobKey(req.Type, req.ID)
	if err := s.store.PutObject(ctx, key, req.Body); err != nil {
		return fmt.Errorf("writing %s: %w", key, err)
	}
	return nil
}

// DeleteBlob removes one blob, idempotently.
//
// (Append-only mode, when it lands, refuses here — for every type but locks.)
func (s *Repository) DeleteBlob(ctx context.Context, req model.DeleteBlobRequest) error {
	return s.deleteObject(ctx, blobKey(req.Type, req.ID))
}

// statObject is the size-or-ErrObjectNotFound both Stat methods answer with.
// The store reports absence as a value rather than an error, so this is where
// that becomes the sentinel the operation turns into a 404.
func (s *Repository) statObject(key string) (int64, error) {
	st, err := s.store.Stat(key)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", key, err)
	}
	if !st.Exist {
		return 0, fmt.Errorf("%w: %s", model.ErrObjectNotFound, key)
	}
	return st.Size, nil
}

// deleteObject removes one object and reports an object that was already gone
// as a success.
//
// That is rest-server's behaviour and restic depends on it — it retries deletes,
// and a retry that lands after the first attempt succeeded must not fail a
// prune. The Storager's Delete is the opposite (a missing object is an error, so
// a caller that meant to delete something learns it did not), so the difference
// is absorbed here rather than in the store.
//
// Deleting straight away and swallowing the absence beats asking Exists first:
// one round trip instead of two, and no window between the question and the act.
func (s *Repository) deleteObject(ctx context.Context, key string) error {
	if err := s.store.Delete(ctx, key); err != nil {
		if !errors.Is(err, storages.ErrFileNotFound) {
			return fmt.Errorf("delete %s: %w", key, err)
		}
		// DEBUG, because this is the normal shape of a retried delete and of a
		// prune finishing work an earlier run started — but it is also what a
		// delete addressing the wrong key looks like, and nothing else would
		// ever say so: the answer on the wire is a 200 either way.
		zerolog.Ctx(ctx).Debug().Str("key", key).Msg("deleted an object that was already gone")
	}
	return nil
}

// blobKey renders the key one blob lives under. Keys are flat — "<type>/<name>"
// — where rest-server shards data/ over 256 sub-directories: that is filesystem
// hygiene, invisible to the protocol, and pointless on an object store.
func blobKey(objectType model.ObjectType, id model.ObjectID) string {
	return string(objectType) + "/" + string(id)
}

// isBlobName keeps a listing to the objects this repository itself writes: keys
// are flat, so a name carrying a separator was put there by something else and
// is not a blob of this repository. rest-server does the same in the other
// direction, skipping the files it finds outside its data/xx shards.
func isBlobName(name string) bool {
	return !strings.Contains(name, "/")
}
