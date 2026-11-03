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

package model

import "fmt"

// EncryptionMarkerVersion is the version an EncryptionMarker is written with.
const EncryptionMarkerVersion = 1

// EncryptionMarker names the public key an encrypted arcave project is encrypted to; no secret.
type EncryptionMarker struct {
	Version   int    `json:"version"`
	Recipient string `json:"recipient"`
}

// NewEncryptionMarker is the marker of an arcave project encrypted to recipient.
func NewEncryptionMarker(recipient string) EncryptionMarker {
	return EncryptionMarker{
		Version:   EncryptionMarkerVersion,
		Recipient: recipient,
	}
}

// Validate refuses a version arcave does not write and a marker naming no recipient.
func (m EncryptionMarker) Validate() error {
	switch {
	case m.Version != EncryptionMarkerVersion:
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidEncryptionMarker, m.Version)
	case m.Recipient == "":
		return fmt.Errorf("%w: no recipient", ErrInvalidEncryptionMarker)
	}
	return nil
}
