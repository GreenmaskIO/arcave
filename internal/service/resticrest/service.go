package resticrest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/storages"
)

// The two conditions the protocol answers with a status of its own, and so the
// whole of what the repository promises beyond "it worked". Everything else it
// returns is a 500 and needs no sentinel.
//
// They are this package's own rather than the store's ErrFileNotFound passed
// through: the handler's contract is with the repository, not with whatever the
// repository keeps its objects in — a double standing in for one has no store
// to borrow a sentinel from. storeRepository is the one layer that compares
// against the library's own sentinel, which is where a library's sentinel is
// matched.
var (
	// errObjectNotFound reports an object this repository does not hold: the
	// 404 for a HEAD, a GET and nothing else — a delete swallows it.
	errObjectNotFound = errors.New("object not found")
	// errConfigExists reports a config that is already written. restic's init
	// depends on the refusal: a repository that already holds data must never
	// be silently re-initialised.
	errConfigExists = errors.New("config already exists")
)

// configKey is the repository config, the one object that lives at the store
// root rather than under a type. It is the storage key, not the URL path the
// gate matches (configPath): the two spell the same word and mean different
// things, and only this file may name the key.
const configKey = "config"

// blob is one object of a listing: the name restic knows it by and its size.
// The json tags are the v2 listing's wire contract — the v1 listing is the same
// objects rendered as bare names, which the handler projects.
type blob struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// storeRepository is one restic repository held in one Storager, and the whole
// of what this package does with storage: it renders the keys, decides which of
// them is a blob of this repository, and reduces a store failure to the two
// conditions the protocol has a status for. Nothing above it names a key.
//
// It holds no state beyond the store, so it is safe to serve concurrently:
// per-key atomicity is the Storager's job.
type storeRepository struct {
	store storages.Storager
}

func newStoreRepository(store storages.Storager) *storeRepository {
	return &storeRepository{store: store}
}

// Create answers `restic init`.
//
// It is a no-op that reports success. rest-server creates the repository
// directory, the five type directories and data/00..ff; a Storager has no
// directories to create — the backend materialises whatever parents a key needs
// when the object is written — so there is nothing to do and nothing that could
// fail. It stays on the seam because a repository that does have something to
// create is the only thing that would need it back.
func (s *storeRepository) Create(ctx context.Context) error {
	zerolog.Ctx(ctx).Debug().Str("store", s.store.GetCwd()).Msg("restic repository created")
	return nil
}

// StatConfig reports the config's size, or errObjectNotFound. restic asks it to
// decide whether a repository is initialised at all.
func (s *storeRepository) StatConfig(_ context.Context) (int64, error) {
	return s.statObject(configKey)
}

// OpenConfig opens the config for reading, whole: it is small, never fetched by
// range, and the caller streams it out unchanged.
func (s *storeRepository) OpenConfig(ctx context.Context) (io.ReadCloser, error) {
	rc, err := s.store.GetObject(ctx, configKey)
	if err != nil {
		if errors.Is(err, storages.ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s", errObjectNotFound, configKey)
		}
		return nil, fmt.Errorf("open %s: %w", configKey, err)
	}
	return rc, nil
}

// SaveConfig writes the config at init time, and refuses to replace one that is
// already there with errConfigExists.
//
// The refusal is the whole point: rest-server opens the file O_EXCL so that a
// restic init can never silently re-initialise a repository that already holds
// data, and restic reports exactly that to the user. The Exists/PutObject pair
// is not atomic and does not need to be — init is a single client doing one
// thing.
func (s *storeRepository) SaveConfig(ctx context.Context, body io.Reader) error {
	exists, err := s.store.Exists(ctx, configKey)
	if err != nil {
		return fmt.Errorf("checking %s: %w", configKey, err)
	}
	if exists {
		return errConfigExists
	}
	if err := s.store.PutObject(ctx, configKey, body); err != nil {
		return fmt.Errorf("writing %s: %w", configKey, err)
	}
	return nil
}

// DeleteConfig removes the config, idempotently.
func (s *storeRepository) DeleteConfig(ctx context.Context) error {
	return s.deleteObject(ctx, configKey)
}

// StatBlob reports one blob's size, or errObjectNotFound. restic asks it to
// decide whether it already holds a pack.
func (s *storeRepository) StatBlob(_ context.Context, objectType, name string) (int64, error) {
	return s.statObject(blobKey(objectType, name))
}

// OpenBlob opens one blob as a seeker, which is what serving a Range over it
// takes. The size is read here, once: a partial response needs it, and
// re-statting per read would make a ranged read cost O(reads) round trips.
func (s *storeRepository) OpenBlob(ctx context.Context, objectType, name string) (io.ReadSeekCloser, error) {
	key := blobKey(objectType, name)
	size, err := s.statObject(key)
	if err != nil {
		return nil, err
	}
	return newStoragerFile(ctx, s.store, key, size), nil
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
func (s *storeRepository) SaveBlob(ctx context.Context, objectType, name string, body io.Reader) error {
	key := blobKey(objectType, name)
	if err := s.store.PutObject(ctx, key, body); err != nil {
		return fmt.Errorf("writing %s: %w", key, err)
	}
	return nil
}

// DeleteBlob removes one blob, idempotently.
//
// (Append-only mode, when it lands, refuses here — for every type but locks.)
func (s *storeRepository) DeleteBlob(ctx context.Context, objectType, name string) error {
	return s.deleteObject(ctx, blobKey(objectType, name))
}

// ListBlobs reports every blob of one type, from a single List of that type's
// prefix.
//
// A type holding nothing is an empty slice, never an error: Create makes no
// directories here, so "absent" and "empty" are the same state, and restic must
// be able to list a fresh repository.
func (s *storeRepository) ListBlobs(ctx context.Context, objectType string) ([]blob, error) {
	stats, err := s.store.List(ctx, objectType)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", objectType, err)
	}

	blobs := make([]blob, 0, len(stats))
	for _, st := range stats {
		if !isBlobName(st.Name) {
			continue
		}
		blobs = append(blobs, blob{Name: st.Name, Size: st.Size})
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

// statObject is the size-or-errObjectNotFound both Stat methods answer with.
// The store reports absence as a value rather than an error, so this is where
// that becomes the sentinel the handler turns into a 404.
func (s *storeRepository) statObject(key string) (int64, error) {
	st, err := s.store.Stat(key)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", key, err)
	}
	if !st.Exist {
		return 0, fmt.Errorf("%w: %s", errObjectNotFound, key)
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
func (s *storeRepository) deleteObject(ctx context.Context, key string) error {
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
func blobKey(objectType, name string) string {
	return objectType + "/" + name
}

// isBlobName keeps a listing to the objects this repository itself writes: keys
// are flat, so a name carrying a separator was put there by something else and
// is not a blob of this repository. rest-server does the same in the other
// direction, skipping the files it finds outside its data/xx shards.
func isBlobName(name string) bool {
	return !strings.Contains(name, "/")
}
