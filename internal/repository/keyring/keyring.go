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

// Package keyring persists an encrypted arcave project's data keys, keys/<key-id>.age: each an
// age file to the store password holding one model.StoreKey.
package keyring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"filippo.io/age"
	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

const keySuffix = ".age"

// Repository is the keys' CRUD, over the keys/ store storefactory.Keys opens.
type Repository struct {
	store storages.Storager
	// workFactor is scrypt's log2 cost a saved key is wrapped with; age's default when 0.
	workFactor int
}

func New(keysStore storages.Storager) *Repository {
	return &Repository{store: keysStore}
}

// SetWorkFactor replaces age's scrypt cost for the keys this repository saves.
func (r *Repository) SetWorkFactor(logN int) *Repository {
	r.workFactor = logN
	return r
}

// ListKeys names the stored keys in ascending order; a file that is no key's is skipped.
func (r *Repository) ListKeys(ctx context.Context) ([]model.StoreKeyID, error) {
	objects, err := r.store.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	ids := make([]model.StoreKeyID, 0, len(objects))
	for _, object := range objects {
		name, isKey := strings.CutSuffix(object.Name, keySuffix)
		id, err := model.ParseStoreKeyID(name)
		if !isKey || err != nil {
			zerolog.Ctx(ctx).Debug().Str("entry", object.Name).Msg("ignoring a file that is no store key")
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

// LoadKey opens key id with password: model.ErrWrongPassword when it does not open it.
func (r *Repository) LoadKey(ctx context.Context, id model.StoreKeyID, password string) (model.StoreKey, error) {
	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return model.StoreKey{}, fmt.Errorf("open key %s: %w", id, err)
	}
	body, err := r.store.GetObject(ctx, keyName(id))
	if err != nil {
		return model.StoreKey{}, fmt.Errorf("read key %s: %w", id, err)
	}
	defer func() {
		if err := body.Close(); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("key", id.String()).Msg("close key")
		}
	}()
	plain, err := age.Decrypt(body, identity)
	var noMatch *age.NoIdentityMatchError
	if errors.As(err, &noMatch) {
		return model.StoreKey{}, fmt.Errorf("open key %s: %w", id, model.ErrWrongPassword)
	}
	if err != nil {
		return model.StoreKey{}, fmt.Errorf("open key %s: %w", id, err)
	}
	var key model.StoreKey
	if err := json.NewDecoder(plain).Decode(&key); err != nil {
		return model.StoreKey{}, fmt.Errorf("decode key %s: %w", id, err)
	}
	if key.ID != id {
		return model.StoreKey{}, fmt.Errorf("key %s names itself %s", id, key.ID)
	}
	return key, nil
}

// SaveKey writes key under password, replacing a key of the same id.
func (r *Repository) SaveKey(ctx context.Context, key model.StoreKey, password string) error {
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return fmt.Errorf("wrap key %s: %w", key.ID, err)
	}
	if r.workFactor > 0 {
		recipient.SetWorkFactor(r.workFactor)
	}
	data, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("encode key %s: %w", key.ID, err)
	}
	ciphertext, err := age.EncryptReader(bytes.NewReader(data), recipient)
	if err != nil {
		return fmt.Errorf("wrap key %s: %w", key.ID, err)
	}
	if err := r.store.PutObject(ctx, keyName(key.ID), ciphertext); err != nil {
		return fmt.Errorf("write key %s: %w", key.ID, err)
	}
	return nil
}

// DeleteKey removes key id.
func (r *Repository) DeleteKey(ctx context.Context, id model.StoreKeyID) error {
	if err := r.store.Delete(ctx, keyName(id)); err != nil {
		return fmt.Errorf("delete key %s: %w", id, err)
	}
	return nil
}

func keyName(id model.StoreKeyID) string {
	return id.String() + keySuffix
}
