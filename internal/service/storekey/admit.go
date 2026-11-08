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

package storekey

import (
	"context"
	"errors"
	"fmt"

	"filippo.io/age"

	"github.com/greenmaskio/arcave/internal/model"
)

// errNotEncryptedYet refuses to read an arcave project no encrypted host has marked: what it holds
// is plain, and reading it encrypted reads nothing.
var errNotEncryptedYet = errors.New("the store is not encrypted yet: an encrypted arcave command marks it on its first write")

// markerReader reads the arcave project's encryption.json.
type markerReader interface {
	Get(ctx context.Context) (model.EncryptionMarker, bool, error)
}

// markerRepository is the arcave project's encryption.json.
type markerRepository interface {
	markerReader
	Put(ctx context.Context, marker model.EncryptionMarker) error
}

// RequirePlain refuses an arcave project an encrypted host has marked: model.ErrStoreEncrypted.
func RequirePlain(ctx context.Context, markers markerReader) error {
	_, found, err := markers.Get(ctx)
	if err != nil {
		return err
	}
	if found {
		return model.ErrStoreEncrypted
	}
	return nil
}

// AdmitEncrypted marks an unmarked arcave project for identity and refuses one marked for
// another key with model.ErrWrongStoreKey.
func AdmitEncrypted(ctx context.Context, markers markerRepository, identity *age.X25519Identity) error {
	found, err := requireRecipient(ctx, markers, identity)
	if err != nil || found {
		return err
	}
	return markers.Put(ctx, model.EncryptionMarker{Recipient: identity.Recipient().String()})
}

// RequireEncrypted is AdmitEncrypted for a reader: it refuses an unmarked arcave project instead of
// marking it, and writes nothing.
func RequireEncrypted(ctx context.Context, markers markerReader, identity *age.X25519Identity) error {
	found, err := requireRecipient(ctx, markers, identity)
	if err != nil {
		return err
	}
	if !found {
		return errNotEncryptedYet
	}
	return nil
}

// requireRecipient refuses a marker naming another key than identity's; found is false for none.
func requireRecipient(ctx context.Context, markers markerReader, identity *age.X25519Identity) (found bool, err error) {
	marker, found, err := markers.Get(ctx)
	if err != nil || !found {
		return false, err
	}
	if marker.Recipient != identity.Recipient().String() {
		return true, fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
	}
	return true, nil
}
