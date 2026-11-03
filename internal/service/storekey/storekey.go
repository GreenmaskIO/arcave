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

// Package storekey generates and parses the store key encryption.key names, and derives the
// restic password from it.
package storekey

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"filippo.io/age"

	"github.com/greenmaskio/arcave/internal/model"
)

const (
	resticPasswordSize = 32
	resticInfo         = "arcave restic v1"
)

// NewKey generates a store key: an age X25519 identity, AGE-SECRET-KEY-1….
func NewKey() (string, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("generate store key: %w", err)
	}
	return identity.String(), nil
}

// ParseKey parses a store key; a malformed one is model.ErrInvalidStoreKey, its text left out.
func ParseKey(key string) (*age.X25519Identity, error) {
	identity, err := age.ParseX25519Identity(key)
	if err != nil {
		return nil, fmt.Errorf("%w: not an age X25519 identity", model.ErrInvalidStoreKey)
	}
	return identity, nil
}

// NewResticPassword derives the password of every restic repository encrypted to identity.
func NewResticPassword(identity *age.X25519Identity) (string, error) {
	derived, err := hkdf.Key(sha256.New, []byte(identity.String()), nil, resticInfo, resticPasswordSize)
	if err != nil {
		return "", fmt.Errorf("derive restic password: %w", err)
	}
	return hex.EncodeToString(derived), nil
}
