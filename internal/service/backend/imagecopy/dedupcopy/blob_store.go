// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dedupcopy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sync"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/utils"
)

// digestAlgorithm is the segment a blob's key opens with, so the key names which
// digest it is. It is the one the archive itself uses; nothing here computes another.
const digestAlgorithm = "sha256"

// blobStore is the shared, content-addressed half of a capture: a blob is named by
// the digest of its own bytes, so two savepoints holding the same layer hold one
// copy. Writes of one digest are serialised — see lock.
type blobStore struct {
	store storages.Storager

	mu    sync.Mutex
	locks map[string]*blobLock
}

// blobLock is one digest's write lock, refcounted so the table does not grow.
type blobLock struct {
	mu   sync.Mutex
	refs int
}

// blobKey is where one digest lives under the blob store root, mirroring the
// archive's own `blobs/sha256/`.
func blobKey(digest string) string {
	return path.Join(digestAlgorithm, digest)
}

func newBlobStore(store storages.Storager) *blobStore {
	return &blobStore{store: store, locks: map[string]*blobLock{}}
}

// put writes one blob unless the store holds it at the size the archive declares — a
// write torn by a killed run is shorter, and rewriting it is the repair; the check is
// sound only under the lock.
func (b *blobStore) put(ctx context.Context, digest string, r io.Reader, size int64) (int64, error) {
	unlock := b.lock(digest)
	defer unlock()

	key := blobKey(digest)
	existing, err := b.store.Stat(key)
	if err != nil {
		return 0, fmt.Errorf("stat blob %s: %w", digest, err)
	}
	if existing != nil && existing.Exist && existing.Size == size {
		// The member's remaining bytes are left to tar.Next, which drops them.
		return 0, nil
	}

	hasher := sha256.New()
	counted := utils.NewCountingReader(io.TeeReader(r, hasher))
	if err := b.store.PutObject(ctx, key, counted); err != nil {
		return 0, fmt.Errorf("write blob %s: %w", digest, err)
	}
	if gotHash := hex.EncodeToString(hasher.Sum(nil)); gotHash != digest {
		// A bad object under a good name in a shared store goes now, not at gc time.
		if err := b.store.Delete(ctx, key); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("blob", digest).
				Msg("a blob that did not verify could not be dropped")
		}
		return 0, fmt.Errorf("blob %s hashes to %s", digest, gotHash)
	}
	return counted.N(), nil
}

// open reads one blob and reports its size.
func (b *blobStore) open(ctx context.Context, digest string) (io.ReadCloser, int64, error) {
	return openSized(ctx, b.store, blobKey(digest))
}

// lock serialises writers of one digest and releases the table entry once the last
// of them is done.
func (b *blobStore) lock(digest string) func() {
	b.mu.Lock()
	entry, ok := b.locks[digest]
	if !ok {
		entry = &blobLock{}
		b.locks[digest] = entry
	}
	entry.refs++
	b.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()

		b.mu.Lock()
		defer b.mu.Unlock()
		entry.refs--
		if entry.refs == 0 {
			delete(b.locks, digest)
		}
	}
}
