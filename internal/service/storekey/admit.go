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

// arcaveProjectContents tells whether the arcave project holds anything but its marker.
type arcaveProjectContents interface {
	HoldsData(ctx context.Context) (bool, error)
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

// RequireEncrypted refuses a project marked for another key (model.ErrWrongStoreKey) or unmarked
// and holding data (model.ErrStoreHoldsPlainData). It writes nothing.
func RequireEncrypted(
	ctx context.Context, markers markerRepository, contents arcaveProjectContents, identity *age.X25519Identity,
) error {
	_, err := requireRecipient(ctx, markers, contents, identity)
	return err
}

// PlainAdmission admits a write to a plain arcave project, refusing one an encrypted host has
// marked since it was opened.
type PlainAdmission struct {
	markers markerRepository
}

// NewPlainAdmission returns a PlainAdmission over markers.
func NewPlainAdmission(markers markerRepository) *PlainAdmission {
	return &PlainAdmission{markers: markers}
}

// AdmitWrite refuses a marked project with model.ErrStoreEncrypted.
func (s *PlainAdmission) AdmitWrite(ctx context.Context) error {
	return RequirePlain(ctx, s.markers)
}

// EncryptedAdmission admits a write to an encrypted arcave project, marking an empty one for identity.
type EncryptedAdmission struct {
	markers  markerRepository
	contents arcaveProjectContents
	identity *age.X25519Identity
}

// NewEncryptedAdmission returns an EncryptedAdmission for identity.
func NewEncryptedAdmission(
	markers markerRepository, contents arcaveProjectContents, identity *age.X25519Identity,
) *EncryptedAdmission {
	return &EncryptedAdmission{
		markers:  markers,
		contents: contents,
		identity: identity,
	}
}

// AdmitWrite refuses as RequireEncrypted does, and marks an unmarked empty project.
func (s *EncryptedAdmission) AdmitWrite(ctx context.Context) error {
	found, err := requireRecipient(ctx, s.markers, s.contents, s.identity)
	if err != nil || found {
		return err
	}
	return s.markers.Put(ctx, model.EncryptionMarker{Recipient: s.identity.Recipient().String()})
}

// requireRecipient refuses a project marked for another key, or an unmarked one holding data;
// found reports a marker for identity.
func requireRecipient(
	ctx context.Context, markers markerRepository, contents arcaveProjectContents, identity *age.X25519Identity,
) (found bool, err error) {
	marker, found, err := markers.Get(ctx)
	if err != nil {
		return false, err
	}
	if found {
		if marker.Recipient != identity.Recipient().String() {
			return false, fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
		}
		return true, nil
	}
	holdsData, err := contents.HoldsData(ctx)
	if err != nil {
		return false, err
	}
	if holdsData {
		return false, model.ErrStoreHoldsPlainData
	}
	return false, nil
}
