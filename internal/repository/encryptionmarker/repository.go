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

// Package encryptionmarker persists an encrypted arcave project's marker, encryption.json at
// the root of its plain store: the one object an encrypted arcave project keeps in plaintext.
package encryptionmarker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/greenmaskio/storages"

	"github.com/greenmaskio/arcave/internal/model"
)

// FileName is the marker's key at the root of the arcave project's plain store.
const FileName = "encryption.json"

// Repository is the marker's CRUD, on the arcave project's store as Root opened it.
type Repository struct {
	store storages.Storager
}

func New(arcaveProjectStore storages.Storager) *Repository {
	return &Repository{store: arcaveProjectStore}
}

// Get reads the marker; found is false when none stands.
func (r *Repository) Get(ctx context.Context) (marker model.EncryptionMarker, found bool, err error) {
	body, err := r.store.GetObject(ctx, FileName)
	if errors.Is(err, storages.ErrFileNotFound) {
		return model.EncryptionMarker{}, false, nil
	}
	if err != nil {
		return model.EncryptionMarker{}, false, fmt.Errorf("open %s: %w", FileName, err)
	}
	data, err := io.ReadAll(body)
	if closeErr := body.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return model.EncryptionMarker{}, false, fmt.Errorf("read %s: %w", FileName, err)
	}
	if err = json.Unmarshal(data, &marker); err != nil {
		return model.EncryptionMarker{}, false, fmt.Errorf("decode %s: %w", FileName, err)
	}
	if marker.Recipient == "" {
		return model.EncryptionMarker{}, false, fmt.Errorf("decode %s: no recipient", FileName)
	}
	return marker, true, nil
}

// Put writes the marker, replacing any existing one.
func (r *Repository) Put(ctx context.Context, marker model.EncryptionMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode %s: %w", FileName, err)
	}
	if err = r.store.PutObject(ctx, FileName, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write %s: %w", FileName, err)
	}
	return nil
}
