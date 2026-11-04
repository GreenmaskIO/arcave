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
	"fmt"

	"filippo.io/age"

	"github.com/greenmaskio/arcave/internal/model"
)

// markerRepository is the arcave project's encryption.json.
type markerRepository interface {
	Get(ctx context.Context) (model.EncryptionMarker, bool, error)
	Put(ctx context.Context, marker model.EncryptionMarker) error
}

// RequirePlain refuses an arcave project an encrypted host has marked: model.ErrStoreEncrypted.
func RequirePlain(ctx context.Context, markers markerRepository) error {
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
	marker, found, err := markers.Get(ctx)
	if err != nil {
		return err
	}
	recipient := identity.Recipient().String()
	switch {
	case !found:
		return markers.Put(ctx, model.EncryptionMarker{Recipient: recipient})
	case marker.Recipient != recipient:
		return fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
	}
	return nil
}
