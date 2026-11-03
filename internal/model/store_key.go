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

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// StoreKeyVersion is the version a StoreKey is written with.
const StoreKeyVersion = 1

// storeKeyIDLength is how many hex digits of its recipient's SHA-256 a key id keeps.
const storeKeyIDLength = 16

// StoreKeyID names a store key: the first 16 hex digits of the SHA-256 of its recipient.
type StoreKeyID string

// NewStoreKeyID is the id of the key whose public recipient is recipient.
func NewStoreKeyID(recipient string) StoreKeyID {
	sum := sha256.Sum256([]byte(recipient))
	return StoreKeyID(hex.EncodeToString(sum[:])[:storeKeyIDLength])
}

// ParseStoreKeyID accepts exactly 16 lowercase hex digits.
func ParseStoreKeyID(s string) (StoreKeyID, error) {
	if len(s) != storeKeyIDLength {
		return "", fmt.Errorf("%w: %q", ErrInvalidStoreKeyID, s)
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: %q", ErrInvalidStoreKeyID, s)
		}
	}
	return StoreKeyID(s), nil
}

func (m StoreKeyID) String() string { return string(m) }

// StoreKey is an encrypted arcave project's data key, kept in the store under its password.
type StoreKey struct {
	Version   int        `json:"version"`
	ID        StoreKeyID `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	// Identity is the age X25519 secret key every object of the arcave project is encrypted to.
	Identity string `json:"identity"`
	// ResticSecret is what every restic repository's password is derived from.
	ResticSecret []byte `json:"restic_secret"`
}
