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

// Package storekey opens an encrypted arcave project's data key with its password, creating
// the key on an arcave project's first use.
package storekey

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// DefaultSettle is how long a new key waits before it reads keys/ back, as a session lock claim does.
const DefaultSettle = time.Second

// maxOpenAttempts bounds how often Open reads keys/ again while it is changing.
const maxOpenAttempts = 5

const (
	resticSecretSize = 32
	resticInfo       = "arcave restic v1"
)

// errKeysGone reports a listing whose every key was deleted before it was read.
var errKeysGone = errors.New("every listed store key is gone")

// keyRepository is the arcave project's keys/.
type keyRepository interface {
	ListKeys(ctx context.Context) ([]model.StoreKeyID, error)
	LoadKey(ctx context.Context, id model.StoreKeyID, password string) (model.StoreKey, error)
	SaveKey(ctx context.Context, key model.StoreKey, password string) error
	DeleteKey(ctx context.Context, id model.StoreKeyID) error
}

// Opener opens store keys, keeping each it opened for the life of the process: a key costs a
// second of scrypt to open, and arcave mcp opens the store on every tool call.
type Opener struct {
	settle time.Duration
	sleep  func(ctx context.Context, d time.Duration) error
	now    func() time.Time

	mu     sync.Mutex
	opened map[openedKey]model.StoreKey
}

func NewOpener() *Opener {
	return &Opener{
		settle: DefaultSettle,
		sleep:  sleepContext,
		now:    time.Now,
		opened: map[openedKey]model.StoreKey{},
	}
}

// SetSettle replaces DefaultSettle.
func (s *Opener) SetSettle(settle time.Duration) *Opener {
	s.settle = settle
	return s
}

// openedKey is what an opened key is kept by: the same key file, opened with the same password.
type openedKey struct {
	id       model.StoreKeyID
	password [sha256.Size]byte
}

// Open returns the key of an encrypted arcave project. A project with no key gets one when
// holdsData finds it empty, and is model.ErrStoreUnencrypted otherwise; a password that opens
// no key is model.ErrWrongPassword, one that opens several model.ErrAmbiguousStoreKey.
func (s *Opener) Open(
	ctx context.Context, keys keyRepository, holdsData func(context.Context) (bool, error), password string,
) (model.StoreKey, error) {
	var last error
	for range maxOpenAttempts {
		ids, err := keys.ListKeys(ctx)
		if err != nil {
			return model.StoreKey{}, err
		}
		if len(ids) > 0 {
			key, err := s.load(ctx, keys, ids, password)
			if !errors.Is(err, model.ErrAmbiguousStoreKey) && !errors.Is(err, errKeysGone) {
				return key, err
			}
			// keys/ is changing: a host giving way deletes its key once its settle is over.
			last = err
			if err := s.sleep(ctx, s.settle); err != nil {
				return model.StoreKey{}, err
			}
			continue
		}
		key, created, err := s.create(ctx, keys, holdsData, password)
		if err != nil || created {
			return key, err
		}
		// Another host wrote a key too: the next attempt finds the one that stayed, or starts
		// again after a wait neither shares.
		last = errors.New("other hosts kept writing one at the same time")
		if err := s.sleep(ctx, mathrand.N(s.settle+1)); err != nil {
			return model.StoreKey{}, err
		}
	}
	return model.StoreKey{}, fmt.Errorf("open the store key: %w", last)
}

// RequireUnencrypted refuses an arcave project that holds a key: model.ErrStoreEncrypted.
func RequireUnencrypted(ctx context.Context, keys keyRepository) error {
	ids, err := keys.ListKeys(ctx)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return fmt.Errorf("key %s: %w", ids[0], model.ErrStoreEncrypted)
	}
	return nil
}

// ParseIdentity is the age identity of key, refused when it is not the one key's id names.
func ParseIdentity(key model.StoreKey) (*age.X25519Identity, error) {
	identity, err := age.ParseX25519Identity(key.Identity)
	if err != nil {
		return nil, fmt.Errorf("parse store key %s: %w", key.ID, err)
	}
	if id := model.NewStoreKeyID(identity.Recipient().String()); id != key.ID {
		return nil, fmt.Errorf("store key %s holds the identity of %s", key.ID, id)
	}
	return identity, nil
}

// NewResticPassword derives the password of every restic repository of key's arcave project.
func NewResticPassword(key model.StoreKey) (string, error) {
	derived, err := hkdf.Key(sha256.New, key.ResticSecret, nil, resticInfo, resticSecretSize)
	if err != nil {
		return "", fmt.Errorf("derive restic password: %w", err)
	}
	return hex.EncodeToString(derived), nil
}

// load opens the one listed key password opens, a key this process already opened first. Keys
// are never chosen between: several the password opens are model.ErrAmbiguousStoreKey, since
// one may be a key another host is about to withdraw; a key gone since the listing is passed
// over, and a listing of none but gone keys is errKeysGone.
func (s *Opener) load(
	ctx context.Context, keys keyRepository, ids []model.StoreKeyID, password string,
) (model.StoreKey, error) {
	for _, id := range ids {
		if key, ok := s.lookup(id, password); ok {
			return key, nil
		}
	}
	var (
		opened  []model.StoreKey
		refused bool
	)
	for _, id := range ids {
		key, err := keys.LoadKey(ctx, id, password)
		switch {
		case errors.Is(err, model.ErrWrongPassword):
			refused = true
			zerolog.Ctx(ctx).Debug().Str("key", id.String()).Msg("the password does not open a store key")
			continue
		case errors.Is(err, storages.ErrFileNotFound):
			zerolog.Ctx(ctx).Debug().Str("key", id.String()).Msg("a store key is gone since the listing")
			continue
		case err != nil:
			return model.StoreKey{}, err
		}
		opened = append(opened, key)
	}
	switch {
	case len(opened) == 1:
		s.keep(opened[0], password)
		return opened[0], nil
	case len(opened) > 1:
		openedIDs := make([]string, len(opened))
		for i, key := range opened {
			openedIDs[i] = key.ID.String()
		}
		return model.StoreKey{}, fmt.Errorf("keys %s: %w", strings.Join(openedIDs, ", "), model.ErrAmbiguousStoreKey)
	case refused:
		return model.StoreKey{}, model.ErrWrongPassword
	default:
		return model.StoreKey{}, errKeysGone
	}
}

// create writes a new key, waits out the settle and reads keys/ back. The key holds only when
// it is the one key there: a host that finds another gives way, deleting its own, so a key
// another host already took is never replaced, however late this host's write landed.
func (s *Opener) create(
	ctx context.Context, keys keyRepository, holdsData func(context.Context) (bool, error), password string,
) (key model.StoreKey, created bool, err error) {
	var (
		taken bool
		ids   []model.StoreKeyID
	)
	if taken, err = requireNoPlainData(ctx, keys, holdsData, ""); err != nil || taken {
		return model.StoreKey{}, false, err
	}
	if key, err = s.newStoreKey(); err != nil {
		return model.StoreKey{}, false, err
	}
	if err = keys.SaveKey(ctx, key, password); err != nil {
		return model.StoreKey{}, false, err
	}
	if err = s.sleep(ctx, s.settle); err != nil {
		return model.StoreKey{}, false, err
	}
	if ids, err = keys.ListKeys(ctx); err != nil {
		return model.StoreKey{}, false, err
	}
	if len(ids) != 1 || ids[0] != key.ID {
		zerolog.Ctx(ctx).Info().Strs("keys", toStrings(ids)).Str("dropped", key.ID.String()).
			Msg("another host wrote a store key at the same time")
		s.withdraw(ctx, keys, key.ID)
		return model.StoreKey{}, false, nil
	}
	taken, err = requireNoPlainData(ctx, keys, holdsData, key.ID)
	if errors.Is(err, model.ErrStoreUnencrypted) || taken {
		s.withdraw(ctx, keys, key.ID)
	}
	if err != nil || taken {
		return model.StoreKey{}, false, err
	}
	zerolog.Ctx(ctx).Info().Str("key", key.ID.String()).Msg("created the store key")
	s.keep(key, password)
	return key, true, nil
}

// requireNoPlainData is model.ErrStoreUnencrypted for an arcave project holding data and no key
// but own. Data beside another host's key is that host's, written encrypted: taken.
func requireNoPlainData(
	ctx context.Context, keys keyRepository, holdsData func(context.Context) (bool, error), own model.StoreKeyID,
) (taken bool, err error) {
	var (
		held bool
		ids  []model.StoreKeyID
	)
	if held, err = holdsData(ctx); err != nil || !held {
		return false, err
	}
	// A host writes its key before its data, so a key behind data is listed by now.
	if ids, err = keys.ListKeys(ctx); err != nil {
		return false, err
	}
	if slices.ContainsFunc(ids, func(id model.StoreKeyID) bool { return id != own }) {
		return true, nil
	}
	return false, model.ErrStoreUnencrypted
}

// withdraw deletes a key this host wrote and gave up; a failure leaves it in keys/, where the
// password opens it beside the key that holds, and every open refuses the two until it goes.
func (s *Opener) withdraw(ctx context.Context, keys keyRepository, id model.StoreKeyID) {
	if err := keys.DeleteKey(ctx, id); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("key", id.String()).Msg("delete a store key given up")
	}
}

func (s *Opener) newStoreKey() (model.StoreKey, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return model.StoreKey{}, fmt.Errorf("generate store key: %w", err)
	}
	secret := make([]byte, resticSecretSize)
	if _, err := rand.Read(secret); err != nil {
		return model.StoreKey{}, fmt.Errorf("generate restic secret: %w", err)
	}
	return model.StoreKey{
		Version:      model.StoreKeyVersion,
		ID:           model.NewStoreKeyID(identity.Recipient().String()),
		CreatedAt:    s.now().UTC(),
		Identity:     identity.String(),
		ResticSecret: secret,
	}, nil
}

func (s *Opener) lookup(id model.StoreKeyID, password string) (model.StoreKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.opened[newOpenedKey(id, password)]
	return key, ok
}

func (s *Opener) keep(key model.StoreKey, password string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened[newOpenedKey(key.ID, password)] = key
}

func newOpenedKey(id model.StoreKeyID, password string) openedKey {
	return openedKey{
		id:       id,
		password: sha256.Sum256([]byte(password)),
	}
}

func toStrings(ids []model.StoreKeyID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
