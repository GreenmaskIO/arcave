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
	"time"

	"filippo.io/age"

	"github.com/greenmaskio/arcave/internal/model"
)

// DefaultSettle is how long a written marker waits before it is read back.
const DefaultSettle = time.Second

// errMarkerGone reports a marker written and not read back after the settle.
var errMarkerGone = errors.New("the encryption marker written did not read back")

// markerRepository is the arcave project's encryption.json.
type markerRepository interface {
	Get(ctx context.Context) (model.EncryptionMarker, bool, error)
	Put(ctx context.Context, marker model.EncryptionMarker) error
}

// Admitter admits an arcave project's store for the encryption section that opens it.
type Admitter struct {
	settle time.Duration
}

// NewAdmitter returns an Admitter waiting DefaultSettle.
func NewAdmitter() *Admitter {
	return &Admitter{settle: DefaultSettle}
}

// SetSettle is how long a written marker waits before it is read back.
func (s *Admitter) SetSettle(settle time.Duration) *Admitter {
	s.settle = settle
	return s
}

// RequirePlain refuses an arcave project an encrypted host has marked: model.ErrStoreEncrypted.
func (s *Admitter) RequirePlain(ctx context.Context, markers markerRepository) error {
	_, found, err := markers.Get(ctx)
	if err != nil {
		return err
	}
	if found {
		return model.ErrStoreEncrypted
	}
	return nil
}

// AdmitEncrypted admits an arcave project marked for identity, marking an unmarked one now:
// no marker is no encryption, whatever lies beside it. A written marker is read back after
// the settle, so a host racing with another key is refused.
func (s *Admitter) AdmitEncrypted(ctx context.Context, markers markerRepository, identity *age.X25519Identity) error {
	recipient := identity.Recipient().String()
	marked, err := requireMarked(ctx, markers, recipient)
	if err != nil || marked {
		return err
	}
	return s.mark(ctx, markers, recipient)
}

// requireMarked reports whether the arcave project is marked for recipient, refusing one
// marked for another; a marker no host wrote whole counts as none.
func requireMarked(ctx context.Context, markers markerRepository, recipient string) (bool, error) {
	marker, found, err := markers.Get(ctx)
	switch {
	case errors.Is(err, model.ErrInvalidEncryptionMarker):
		return false, nil
	case err != nil:
		return false, err
	case !found:
		return false, nil
	case marker.Recipient != recipient:
		return false, fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
	}
	return true, nil
}

// mark marks the arcave project for recipient and reads the marker back after the settle.
func (s *Admitter) mark(ctx context.Context, markers markerRepository, recipient string) error {
	if err := markers.Put(ctx, model.NewEncryptionMarker(recipient)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(s.settle):
	}
	marker, found, err := markers.Get(ctx)
	if err != nil {
		return err
	}
	if !found {
		return errMarkerGone
	}
	if marker.Recipient != recipient {
		return fmt.Errorf("%w: %s", model.ErrWrongStoreKey, marker.Recipient)
	}
	return nil
}
